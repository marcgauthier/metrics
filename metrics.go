// Package metrics provides embedded, compressed, indexed time-series storage
// using VictoriaMetrics' storage engine. It does not run a server or expose
// network ports.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marcgauthier/logmetrics/internal/cryptofs"
	"github.com/marcgauthier/logmetrics/internal/vm/lib/prompb"
	"github.com/marcgauthier/logmetrics/internal/vm/lib/storage"
)

var (
	// ErrClosed is returned when an operation is attempted after Close.
	ErrClosed = errors.New("metrics: database is closed")
	// ErrReadOnly is returned when the storage engine has stopped accepting writes.
	ErrReadOnly = errors.New("metrics: storage is read-only")
	// ErrExpiredTimestamp indicates that a sample is older than configured retention.
	ErrExpiredTimestamp = errors.New("metrics: sample is older than retention")
	// ErrFutureTimestamp indicates that a sample is too far in the future.
	ErrFutureTimestamp = errors.New("metrics: sample timestamp is more than 24 hours in the future")
	// ErrTooManySeries indicates that a search matched more series than the
	// caller's maxSeries cap. Narrow the selector or time range, or raise
	// the cap.
	ErrTooManySeries = errors.New("metrics: too many matching series")
)

// tooManySeriesFragment identifies the engine's series-cap error. The engine
// offers no truncation mode: exceeding maxMetrics fails the search.
const tooManySeriesFragment = "number of matching timeseries exceeds"

// precisionBits stores float64 values without information loss, matching the
// VictoriaMetrics server default.
const precisionBits = 64

// unlimitedSeries is the series cap for cap-less searches. It mirrors the
// engine's own default for unlimited queries; math.MaxInt overflows the
// engine's internal cap arithmetic and fails every search.
const unlimitedSeries = 2e9

// maxSeriesIdentityBytes bounds len(metric)+len(key)+len(value) for every
// label so the engine's composite index items always fit its 64KiB block.
// The worst-case item overhead is 31 bytes (prefixes, length varints and the
// metric ID); the 64-byte margin keeps every accepted series searchable.
// Without this bound the engine silently drops the index rows of oversized
// series.
const maxSeriesIdentityBytes = 65536 - 64

// Sample is a single time-series data point. A zero Time is replaced with the
// current time during ingestion; sub-millisecond precision is truncated because
// the storage engine uses millisecond timestamps.
type Sample struct {
	Metric string
	Labels map[string]string
	Time   time.Time
	Value  float64
}

// Point is a stored timestamp/value pair. Time is always in UTC with
// millisecond precision.
type Point struct {
	Time  time.Time
	Value float64
}

// Series is a metric name plus its label set and the points stored for it in
// the selected time range, in ascending time order.
type Series struct {
	Metric string
	Labels map[string]string
	Points []Point
}

// Selector matches time series by metric name and exact label values. An empty
// Metric matches any metric name; an empty Labels matches any label set.
type Selector struct {
	Metric string
	Labels map[string]string
}

// TimeRange bounds a selection. A zero Start selects from the oldest retained
// data; a zero End selects up to the current time. Samples with future
// timestamps need an explicit End past their timestamps to be selected.
type TimeRange struct {
	Start time.Time
	End   time.Time
}

// DB is a concurrency-safe handle to a single local metrics directory.
// Close waits for active writes and queries to finish.
type DB struct {
	mu      sync.RWMutex
	storage *storage.Storage
	options config
	release func()
	closed  bool
}

// Stats contains a small, stable subset of the storage statistics. The Rows
// counters accumulate since Open and reset when the database is reopened;
// Series always reflects the current on-disk state.
type Stats struct {
	Series            uint64
	RowsReceived      uint64
	RowsAdded         uint64
	RowsDroppedTooOld uint64
	RowsDroppedTooNew uint64
	ReadOnly          bool
}

// Open opens or creates an embedded metrics database in dir. All settings are
// provided through functional options; no HTTP service or CLI flags are used.
//
// Open-time storage failures, including a locked or unwritable directory,
// are returned as errors. After a successful Open the engine may still panic
// on unrecoverable runtime storage failures.
func Open(dir string, options ...Option) (*DB, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("metrics: directory must not be empty")
	}
	cfg := config{
		retentionDays: 7,
		flushInterval: 2 * time.Second,
	}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("metrics: nil option")
		}
		if err := option(&cfg); err != nil {
			return nil, err
		}
	}
	path, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("metrics: resolve directory: %w", err)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, fmt.Errorf("metrics: create directory: %w", err)
	}
	var release func()
	if len(cfg.encryptionKey) > 0 {
		release, err = cryptofs.Unlock(path, cfg.encryptionKey)
		// Do not retain the caller's raw key in the long-lived DB options;
		// only the initialized AES-GCM cipher state stays registered.
		clear(cfg.encryptionKey)
		cfg.encryptionKey = nil
		if err != nil {
			return nil, err
		}
	} else {
		if err := cryptofs.RequireKey(path); err != nil {
			return nil, err
		}
	}
	// These engine settings are process-wide; the last DB opened wins.
	storage.SetDataFlushInterval(cfg.flushInterval)
	storage.SetFreeDiskSpaceLimit(cfg.minFreeDiskBytes)
	opened := false
	defer func() {
		if !opened && release != nil {
			release()
		}
	}()
	s, err := openStorage(path, storage.OpenOptions{
		Retention:      time.Duration(cfg.retentionDays) * 24 * time.Hour,
		MaxBackfillAge: time.Duration(cfg.retentionDays) * 24 * time.Hour,
		// FutureRetention is left at zero: the engine clamps it to a 48-hour
		// minimum while Write/WriteBatch enforce a stricter 24-hour cap.
	})
	if err != nil {
		return nil, err
	}
	opened = true
	return &DB{storage: s, options: cfg, release: release}, nil
}

// openStorage opens the engine storage, converting its Must-style panics
// (locked directory, unwritable path, corrupt storage) into errors.
func openStorage(path string, opts storage.OpenOptions) (s *storage.Storage, err error) {
	defer func() {
		if r := recover(); r != nil {
			s = nil
			err = fmt.Errorf("metrics: open storage: %v", r)
		}
	}()
	s = storage.MustOpenStorage(path, opts)
	return s, nil
}

// Write stores one sample. Search visibility is asynchronous: recently written
// samples become searchable within a couple of seconds, or immediately after
// Flush. The sample is copied into the engine's buffers.
func (db *DB) Write(sample Sample) error {
	return db.WriteBatch([]Sample{sample})
}

// WriteBatch ingests samples in bounded groups to minimize ingestion overhead.
// Invalid samples are rejected before any sample in this call is submitted.
// An accepted sample may still be dropped by the engine for engine-level
// limits; see Stats for the drop counters.
func (db *DB) WriteBatch(samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return ErrClosed
	}
	if db.storage.IsReadOnly() {
		return ErrReadOnly
	}

	now := time.Now()
	rows := make([]storage.MetricRow, len(samples))
	for i, sample := range samples {
		ts := sample.Time
		if ts.IsZero() {
			ts = now
		}
		if err := checkSample(sample, ts, now, db.options.retentionDays); err != nil {
			return fmt.Errorf("metrics: sample %d: %w", i, err)
		}
		rows[i] = storage.MetricRow{
			MetricNameRaw: marshalMetricName(sample.Metric, sample.Labels),
			Timestamp:     ts.UnixMilli(),
			Value:         sample.Value,
		}
	}

	const batchSize = 512
	for start := 0; start < len(rows); start += batchSize {
		end := min(start+batchSize, len(rows))
		db.storage.AddRows(rows[start:end], precisionBits)
	}
	return nil
}

func checkSample(sample Sample, ts, now time.Time, retentionDays int) error {
	if strings.TrimSpace(sample.Metric) == "" {
		return errors.New("metric name must not be empty")
	}
	if strings.HasPrefix(sample.Metric, "__") {
		return fmt.Errorf("invalid metric name %q (names starting with __ are reserved)", sample.Metric)
	}
	if len(sample.Metric) > math.MaxUint16 {
		return fmt.Errorf("metric name is %d bytes, exceeding the 65535-byte limit", len(sample.Metric))
	}
	if len(sample.Metric) > maxSeriesIdentityBytes {
		return fmt.Errorf("metric name is %d bytes, exceeding the %d-byte series identity limit", len(sample.Metric), maxSeriesIdentityBytes)
	}
	for key, value := range sample.Labels {
		if key == "" {
			return errors.New("label name must not be empty")
		}
		if key == "__name__" || strings.HasPrefix(key, "__") {
			return fmt.Errorf("invalid label name %q (names starting with __ are reserved)", key)
		}
		if len(key) > math.MaxUint16 {
			return fmt.Errorf("label name %q exceeds the 65535-byte limit", key[:64])
		}
		if value == "" {
			return fmt.Errorf("label %q must not have an empty value", key)
		}
		if len(value) > math.MaxUint16 {
			return fmt.Errorf("label %q value exceeds the 65535-byte limit", key)
		}
		// The engine builds one composite index item per label holding the
		// metric name plus that label; oversized items are silently dropped.
		if identity := len(sample.Metric) + len(key) + len(value); identity > maxSeriesIdentityBytes {
			return fmt.Errorf("series identity for label %q is %d bytes, exceeding the %d-byte limit", key, identity, maxSeriesIdentityBytes)
		}
	}
	if math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
		return fmt.Errorf("invalid value %v (must be a finite number)", sample.Value)
	}
	if ts.Before(now.Add(-time.Duration(retentionDays) * 24 * time.Hour)) {
		return ErrExpiredTimestamp
	}
	if ts.After(now.Add(24 * time.Hour)) {
		return ErrFutureTimestamp
	}
	return nil
}

// marshalMetricName encodes a metric name and label set in the engine's raw
// ingestion format. Labels are sorted by name for a deterministic encoding;
// the engine canonicalizes the series identity during indexing.
func marshalMetricName(metric string, labels map[string]string) []byte {
	// MarshalMetricNameRaw mutates the label slice (it clears the __name__
	// label), so it always receives a freshly built slice.
	raw := make([]prompb.Label, 0, len(labels)+1)
	raw = append(raw, prompb.Label{Name: "__name__", Value: metric})
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		raw = append(raw, prompb.Label{Name: k, Value: labels[k]})
	}
	return storage.MarshalMetricNameRaw(nil, raw)
}

// Select returns the time series matching sel within tr, ordered by metric
// name and labels. Every returned series holds all of its points in tr in
// ascending time order. Select fails with ErrTooManySeries when more than
// maxSeries series match; it never returns a truncated set silently. The cap
// counts series indexed on the days covered by tr, which may include series
// with no points inside tr itself.
func (db *DB) Select(ctx context.Context, sel Selector, tr TimeRange, maxSeries int) ([]Series, error) {
	if maxSeries <= 0 {
		return nil, errors.New("metrics: max series must be positive")
	}
	if err := checkSelector(sel); err != nil {
		return nil, err
	}
	series, err := db.runSearch(ctx, sel, tr, maxSeries)
	if err != nil {
		return nil, err
	}
	return series, nil
}

// Query invokes visit for each time series matching sel within tr, ordered by
// metric name and labels. Results are merged before visiting, so every series
// is visited exactly once with all of its points in tr. Visiting happens
// after the read completes, so the callback may safely call DB methods.
// Return an error to stop visiting.
func (db *DB) Query(ctx context.Context, sel Selector, tr TimeRange, visit func(Series) error) error {
	if visit == nil {
		return errors.New("metrics: visit callback is nil")
	}
	if err := checkSelector(sel); err != nil {
		return err
	}
	series, err := db.runSearch(ctx, sel, tr, unlimitedSeries)
	if err != nil {
		return err
	}
	for _, s := range series {
		if err := visit(s); err != nil {
			return err
		}
	}
	return nil
}

// Latest returns the most recent stored point of every series matching sel,
// ordered by metric name and labels. Series without retained points are
// omitted. Latest fails with ErrTooManySeries when more than maxSeries
// series match.
func (db *DB) Latest(ctx context.Context, sel Selector, maxSeries int) ([]Series, error) {
	if maxSeries <= 0 {
		return nil, errors.New("metrics: max series must be positive")
	}
	if err := checkSelector(sel); err != nil {
		return nil, err
	}
	db.mu.RLock()
	retention := time.Duration(db.options.retentionDays) * 24 * time.Hour
	db.mu.RUnlock()
	now := time.Now()
	series, err := db.runSearch(ctx, sel, TimeRange{Start: now.Add(-retention), End: now}, maxSeries)
	if err != nil {
		return nil, err
	}
	latest := series[:0]
	for _, s := range series {
		if len(s.Points) == 0 {
			continue
		}
		s.Points = s.Points[len(s.Points)-1:]
		latest = append(latest, s)
	}
	return latest, nil
}

func checkSelector(sel Selector) error {
	if strings.HasPrefix(sel.Metric, "__") && sel.Metric != "" {
		return fmt.Errorf("metrics: invalid metric name %q (names starting with __ are reserved)", sel.Metric)
	}
	for key, value := range sel.Labels {
		if key == "" {
			return errors.New("metrics: label name must not be empty")
		}
		if key == "__name__" || strings.HasPrefix(key, "__") {
			return fmt.Errorf("metrics: invalid label name %q (names starting with __ are reserved)", key)
		}
		if value == "" {
			return fmt.Errorf("metrics: label %q must not have an empty value", key)
		}
	}
	return nil
}

func (db *DB) runSearch(ctx context.Context, sel Selector, tr TimeRange, maxSeries int) ([]Series, error) {
	if ctx == nil {
		return nil, errors.New("metrics: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tfs := storage.NewTagFilters()
	if sel.Metric != "" {
		// The engine encodes the metric-name filter with a nil key.
		if err := tfs.Add(nil, []byte(sel.Metric), false, false); err != nil {
			return nil, fmt.Errorf("metrics: metric filter: %w", err)
		}
	}
	keys := make([]string, 0, len(sel.Labels))
	for k := range sel.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := tfs.Add([]byte(k), []byte(sel.Labels[k]), false, false); err != nil {
			return nil, fmt.Errorf("metrics: label filter: %w", err)
		}
	}
	engineTR, err := toEngineTimeRange(tr)
	if err != nil {
		return nil, err
	}
	deadline := searchDeadline(ctx)

	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return nil, ErrClosed
	}

	var sr storage.Search
	sr.Init(nil, db.storage, []*storage.TagFilters{tfs}, engineTR, maxSeries, deadline)
	merged := make(map[string]*Series)
	var order []string
	var runErr error
	for runErr == nil && sr.NextMetricBlock() {
		if ctx.Err() != nil {
			runErr = ctx.Err()
			break
		}
		mb := &sr.MetricBlockRef
		var mn storage.MetricName
		if err := mn.Unmarshal(mb.MetricName); err != nil {
			runErr = fmt.Errorf("metrics: decode series: %w", err)
			break
		}
		var b storage.Block
		mb.BlockRef.MustReadBlock(&b)
		if err := b.UnmarshalData(); err != nil {
			runErr = fmt.Errorf("metrics: decode block: %w", err)
			break
		}
		timestamps, values := b.AppendRowsWithTimeRangeFilter(nil, nil, engineTR)
		if len(timestamps) == 0 {
			continue
		}
		key := string(mb.MetricName)
		s, ok := merged[key]
		if !ok {
			s = &Series{
				Metric: string(append([]byte(nil), mn.MetricGroup...)),
				Labels: make(map[string]string, len(mn.Tags)),
			}
			for _, tag := range mn.Tags {
				s.Labels[string(append([]byte(nil), tag.Key...))] = string(append([]byte(nil), tag.Value...))
			}
			merged[key] = s
			order = append(order, key)
		}
		for i, ts := range timestamps {
			s.Points = append(s.Points, Point{
				Time:  time.UnixMilli(ts).UTC(),
				Value: values[i],
			})
		}
	}
	// MustClose resets the search, so the search error must be read first.
	searchErr := sr.Error()
	sr.MustClose()
	if runErr != nil {
		return nil, runErr
	}
	if searchErr != nil {
		if strings.Contains(searchErr.Error(), tooManySeriesFragment) {
			return nil, fmt.Errorf("%w: matched more than %d series", ErrTooManySeries, maxSeries)
		}
		return nil, fmt.Errorf("metrics: execute search: %w", searchErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	series := make([]Series, 0, len(order))
	for _, key := range order {
		s := merged[key]
		// Blocks may overlap in time; order points and collapse duplicate
		// timestamps deterministically, keeping the last value seen.
		sort.SliceStable(s.Points, func(i, j int) bool {
			return s.Points[i].Time.Before(s.Points[j].Time)
		})
		points := s.Points[:0]
		for _, p := range s.Points {
			if n := len(points); n > 0 && points[n-1].Time.Equal(p.Time) {
				points[n-1] = p
				continue
			}
			points = append(points, p)
		}
		s.Points = points
		series = append(series, *s)
	}
	sort.Slice(series, func(i, j int) bool {
		if series[i].Metric != series[j].Metric {
			return series[i].Metric < series[j].Metric
		}
		return seriesLabelsKey(series[i].Labels) < seriesLabelsKey(series[j].Labels)
	})
	return series, nil
}

func seriesLabelsKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte(0)
		sb.WriteString(labels[k])
		sb.WriteByte(0)
	}
	return sb.String()
}

func toEngineTimeRange(tr TimeRange) (storage.TimeRange, error) {
	var out storage.TimeRange
	if tr.Start.IsZero() {
		out.MinTimestamp = 0
	} else {
		out.MinTimestamp = tr.Start.UnixMilli()
	}
	if tr.End.IsZero() {
		out.MaxTimestamp = time.Now().UnixMilli()
	} else {
		out.MaxTimestamp = tr.End.UnixMilli()
	}
	if out.MinTimestamp > out.MaxTimestamp {
		return storage.TimeRange{}, errors.New("metrics: time range start must not be after end")
	}
	return out, nil
}

// searchDeadline converts a context deadline into the engine's unix-seconds
// search deadline, or disables the engine timeout when the context has none.
// Cancellation is still honored between blocks via ctx.Err.
func searchDeadline(ctx context.Context) uint64 {
	if deadline, ok := ctx.Deadline(); ok {
		sec := deadline.Unix()
		if sec < 0 {
			return 0
		}
		return uint64(sec)
	}
	return ^uint64(0)
}

// Flush forces buffered samples to become searchable. This is an expensive
// debugging/testing operation, not a routine durability API; normal operation
// relies on the background flush cycle.
func (db *DB) Flush() error {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return ErrClosed
	}
	db.storage.DebugFlush()
	return nil
}

// Stats returns current storage statistics.
func (db *DB) Stats() (Stats, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return Stats{}, ErrClosed
	}
	series, err := db.storage.GetSeriesCount(^uint64(0))
	if err != nil {
		return Stats{}, fmt.Errorf("metrics: count series: %w", err)
	}
	var m storage.Metrics
	db.storage.UpdateMetrics(&m)
	return Stats{
		Series:            series,
		RowsReceived:      m.RowsReceivedTotal,
		RowsAdded:         m.RowsAddedTotal,
		RowsDroppedTooOld: m.TooSmallTimestampRows,
		RowsDroppedTooNew: m.TooBigTimestampRows,
		ReadOnly:          db.storage.IsReadOnly(),
	}, nil
}

// Close stops the storage engine, flushes its managed state and releases the
// exclusive directory lock. Close is safe to call multiple times.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil
	}
	db.storage.MustClose()
	if db.release != nil {
		db.release()
		db.release = nil
	}
	db.closed = true
	return nil
}

// Unlock opens an encrypted database with a 32-byte raw AES-256-GCM key.
// It is a convenience for Open(dir, WithEncryptionKey(key), opts...).
// The key must be supplied before storage is opened; a wrong key is rejected
// during unlock, before any encrypted index files are read.
func Unlock(dir string, key []byte, options ...Option) (*DB, error) {
	opts := append([]Option{WithEncryptionKey(key)}, options...)
	return Open(dir, opts...)
}
