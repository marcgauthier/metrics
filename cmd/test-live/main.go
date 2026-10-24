// Command test-live runs an encrypted continuous-ingestion soak test.
//
// It writes synthetic samples at a steady rate for a fixed duration (default
// 45 minutes at 1000 samples/s), reports progress, then verifies the data and
// the on-disk encryption envelopes. It exits non-zero on any failure.
//
// The requested retention is honored down to the storage engine minimum:
// the API minimum is 1 day, so a value such as 5m runs with 1-day retention
// and nothing is expected to expire during the soak.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/marcgauthier/logmetrics"
)

var (
	dirFlag       = flag.String("dir", "", "database directory (default temp directory, removed on exit unless -keep)")
	keepFlag      = flag.Bool("keep", false, "retain an auto-created temp directory after the run")
	durationFlag  = flag.Duration("duration", 45*time.Minute, "how long to ingest continuously")
	rateFlag      = flag.Int("rate", 1000, "target samples per second")
	retentionFlag = flag.Duration("retention", 5*time.Minute, "requested retention (engine minimum is 24h)")
	reportFlag    = flag.Duration("report", 30*time.Second, "progress report interval")
	keyHexFlag    = flag.String("key-hex", "", "32-byte AES key as 64 hex chars (or METRICS_TESTLIVE_KEY_HEX)")
	keyFileFlag   = flag.String("key-file", "", "path to a file holding the raw 32-byte AES key")
)

var (
	metricNames = []string{"http_requests_total", "cpu_temperature", "go_gc_heap_bytes", "jobs_completed_total"}
	services    = []string{"gateway", "auth", "inventory", "scheduler", "agent"}
)

func main() {
	flag.Parse()
	if err := execute(); err != nil {
		fmt.Fprintf(os.Stderr, "test-live: %v\n", err)
		os.Exit(1)
	}
}

func execute() error {
	if *durationFlag <= 0 {
		return fmt.Errorf("duration must be positive, got %v", *durationFlag)
	}
	if *rateFlag < 1 {
		return fmt.Errorf("rate must be at least 1 sample/s, got %d", *rateFlag)
	}
	if *retentionFlag <= 0 {
		return fmt.Errorf("retention must be positive, got %v", *retentionFlag)
	}
	if *reportFlag < time.Second {
		return fmt.Errorf("report interval must be at least 1s, got %v", *reportFlag)
	}
	key, ephemeral, err := resolveKey()
	if err != nil {
		return err
	}
	defer clear(key)

	days := retentionDays(*retentionFlag)
	if *retentionFlag < 24*time.Hour {
		fmt.Printf("WARNING: requested retention %v is below the engine minimum; running with %d-day retention (nothing expires during this soak)\n", *retentionFlag, days)
	}

	dir, remove, err := prepareDir()
	if err != nil {
		return err
	}
	if remove {
		defer os.RemoveAll(dir)
	}

	runTagBytes := make([]byte, 4)
	if _, err := rand.Read(runTagBytes); err != nil {
		return fmt.Errorf("generate run tag: %w", err)
	}
	runTag := "testlive" + hex.EncodeToString(runTagBytes)

	sum := sha256.Sum256(key)
	fmt.Printf("test-live: dir=%s duration=%v rate=%d/s retention=%v(effective %dd) key=%s sha256=%.16x run=%s\n",
		dir, *durationFlag, *rateFlag, *retentionFlag, days, keySource(ephemeral), sum, runTag)

	db, err := metrics.Unlock(dir, key,
		metrics.WithRetentionDays(days),
		metrics.WithFlushInterval(time.Second),
	)
	if err != nil {
		return fmt.Errorf("open encrypted database: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	start := time.Now()
	result := ingest(ctx, db, dir, start, runTag)
	interrupted := ctx.Err() != nil && result.elapsed < *durationFlag
	if result.err != nil {
		db.Close()
		return result.err
	}
	fmt.Printf("ingest finished: wrote=%d target_rate=%d/s elapsed=%v interrupted=%v\n",
		result.written, *rateFlag, result.elapsed.Round(time.Second), interrupted)

	if err := db.Flush(); err != nil {
		db.Close()
		return fmt.Errorf("flush: %w", err)
	}
	stats, err := db.Stats()
	if err != nil {
		db.Close()
		return fmt.Errorf("stats: %w", err)
	}
	diskBytes, err := dirSize(dir)
	if err != nil {
		db.Close()
		return err
	}
	fmt.Printf("final: series=%d rows_added=%d rows_received=%d dropped_old=%d dropped_new=%d disk=%.2fMiB\n",
		stats.Series, stats.RowsAdded, stats.RowsReceived, stats.RowsDroppedTooOld, stats.RowsDroppedTooNew,
		float64(diskBytes)/(1024*1024))
	if err := verifyCounts(db, runTag, result.written); err != nil {
		db.Close()
		return err
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}

	if err := verifyEncrypted(dir, runTag); err != nil {
		return err
	}
	if err := verifyReopen(dir, key, days, runTag, result.written); err != nil {
		return err
	}
	fmt.Println("test-live: PASS")
	return nil
}

type ingestResult struct {
	written uint64
	elapsed time.Duration
	err     error
}

func ingest(ctx context.Context, db *metrics.DB, dir string, start time.Time, runTag string) ingestResult {
	deadline := start.Add(*durationFlag)
	target := uint64(float64(*rateFlag) * (*durationFlag).Seconds())
	// One distinct millisecond per sample, all in the past: the span is
	// target milliseconds ending a minute before the run starts.
	base := start.Add(-time.Duration(target) * time.Millisecond).Add(-time.Minute)
	const tickInterval = 100 * time.Millisecond
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	reporter := time.NewTicker(*reportFlag)
	defer reporter.Stop()

	var written uint64
	var seq uint64
	var latencies []time.Duration
	batch := make([]metrics.Sample, 0, max(*rateFlag/10+2, 16))

	report := func(now time.Time) {
		elapsed := now.Sub(start)
		stats, err := db.Stats()
		if err != nil {
			fmt.Printf("[%v] stats error: %v\n", elapsed.Round(time.Second), err)
			return
		}
		disk, _ := dirSize(dir)
		fmt.Printf("[%v/%v] written=%d actual=%.0f/s batch_p50=%v batch_p95=%v series=%d rows_added=%d dropped_old=%d disk=%.1fMiB\n",
			elapsed.Round(time.Second), (*durationFlag).Round(time.Second), written,
			float64(written)/max1(elapsed.Seconds()), percentile(latencies, 0.50),
			percentile(latencies, 0.95), stats.Series, stats.RowsAdded, stats.RowsDroppedTooOld,
			float64(disk)/(1024*1024))
	}

	for {
		select {
		case <-ctx.Done():
			return ingestResult{written: written, elapsed: time.Since(start)}
		case now := <-reporter.C:
			report(now)
		case now := <-ticker.C:
			if !now.Before(deadline) {
				// Drain the final partial tick so the run hits its target.
				for written < target {
					batch = batch[:0]
					for i := 0; i < 512 && written < target; i++ {
						batch = append(batch, makeSample(seq, runTag, base))
						seq++
						written++
					}
					if err := db.WriteBatch(batch); err != nil {
						return ingestResult{written: written, elapsed: time.Since(start),
							err: fmt.Errorf("write final batch at seq=%d: %w", seq, err)}
					}
				}
				report(time.Now())
				return ingestResult{written: written, elapsed: time.Since(start)}
			}
			// Deficit pacing: derive the batch size from elapsed time so
			// slow ticks are caught up instead of dropping throughput.
			expected := uint64(float64(*rateFlag) * time.Since(start).Seconds())
			if target := uint64(float64(*rateFlag) * (*durationFlag).Seconds()); expected > target {
				expected = target
			}
			n := batchSize(expected, written, nominalPerTick())
			if n == 0 {
				continue
			}
			batch = batch[:0]
			for i := 0; i < n; i++ {
				batch = append(batch, makeSample(seq, runTag, base))
				seq++
			}
			writeStart := time.Now()
			if err := db.WriteBatch(batch); err != nil {
				return ingestResult{written: written, elapsed: time.Since(start),
					err: fmt.Errorf("write batch at seq=%d: %w", seq, err)}
			}
			latencies = append(latencies, time.Since(writeStart))
			if len(latencies) > 600 {
				latencies = latencies[len(latencies)-600:]
			}
			written += uint64(n)
		}
	}
}

// nominalPerTick is the sample count for one pacing tick at the target rate.
func nominalPerTick() int {
	return max(*rateFlag/10, 1)
}

// batchSize returns how many samples to write this tick: the pacing deficit,
// capped so a long stall (slow engine, slow progress report) cannot build
// one unbounded batch. Unwritten deficit carries to later ticks.
func batchSize(expected, written uint64, nominal int) int {
	if expected <= written {
		return 0
	}
	deficit := expected - written
	maxPerTick := min(uint64(max(nominal, 1))*10, 1<<20)
	maxPerTick = max(maxPerTick, 1024)
	if deficit > maxPerTick {
		deficit = maxPerTick
	}
	return int(deficit)
}

func makeSample(seq uint64, runTag string, base time.Time) metrics.Sample {
	// Distinct millisecond per sample across the whole run: base is one
	// minute before the earliest sample, so no two samples of a series ever
	// share a timestamp (no deduplication can hide a lost write) and every
	// sample stays in the past regardless of the ingest rate.
	ts := base.Add(time.Duration(seq) * time.Millisecond)
	return metrics.Sample{
		Metric: metricNames[seq%uint64(len(metricNames))],
		Labels: map[string]string{
			"service": services[(seq*13+seq/11)%uint64(len(services))],
			"host":    fmt.Sprintf("edge-%03d", (seq*37)%64),
			"run_id":  runTag,
		},
		Time:  ts,
		Value: float64(seq % 1000),
	}
}

// retentionDays converts a requested retention to whole days, rounding up to
// the storage engine minimum of one day.
func retentionDays(d time.Duration) int {
	days := int((d + 24*time.Hour - time.Nanosecond) / (24 * time.Hour))
	return max(days, 1)
}

func resolveKey() (key []byte, ephemeral bool, err error) {
	if *keyHexFlag != "" && *keyFileFlag != "" {
		return nil, false, fmt.Errorf("only one of -key-hex and -key-file may be set")
	}
	if h := strings.TrimSpace(*keyHexFlag); h != "" {
		raw, err := hex.DecodeString(h)
		if err != nil || len(raw) != 32 {
			return nil, false, fmt.Errorf("-key-hex must be 64 hex chars (32 bytes)")
		}
		return raw, false, nil
	}
	if h := strings.TrimSpace(os.Getenv("METRICS_TESTLIVE_KEY_HEX")); h != "" && *keyFileFlag == "" {
		raw, err := hex.DecodeString(h)
		if err != nil || len(raw) != 32 {
			return nil, false, fmt.Errorf("METRICS_TESTLIVE_KEY_HEX must be 64 hex chars (32 bytes)")
		}
		return raw, false, nil
	}
	if *keyFileFlag != "" {
		raw, err := os.ReadFile(*keyFileFlag)
		if err != nil {
			return nil, false, fmt.Errorf("read key file: %w", err)
		}
		if len(raw) != 32 {
			return nil, false, fmt.Errorf("key file must hold exactly 32 bytes, got %d", len(raw))
		}
		return raw, false, nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, false, fmt.Errorf("generate ephemeral key: %w", err)
	}
	return raw, true, nil
}

func keySource(ephemeral bool) string {
	if ephemeral {
		return "ephemeral"
	}
	return "provided"
}

func prepareDir() (dir string, remove bool, err error) {
	if *dirFlag == "" {
		dir, err = os.MkdirTemp("", "test-live-*")
		if err != nil {
			return "", false, err
		}
		return dir, !*keepFlag, nil
	}
	dir = *dirFlag
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", false, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false, err
	}
	if len(entries) > 0 {
		return "", false, fmt.Errorf("directory %s is not empty; test-live requires an empty directory", dir)
	}
	return dir, false, nil
}

// verifyCounts proves every written sample is selectable exactly once.
func verifyCounts(db *metrics.DB, runTag string, written uint64) error {
	var series, points int
	err := db.Query(context.Background(), metrics.Selector{Labels: map[string]string{"run_id": runTag}}, metrics.TimeRange{},
		func(s metrics.Series) error {
			series++
			points += len(s.Points)
			return nil
		})
	if err != nil {
		return fmt.Errorf("verify counts: %w", err)
	}
	if uint64(points) != written {
		return fmt.Errorf("verify counts: got %d points in %d series, want %d", points, series, written)
	}
	fmt.Printf("count check: %d points in %d series, all present\n", points, series)
	return nil
}

// verifyEncrypted checks every storage file carries the encryption envelope
// and that the run tag never appears as plaintext on disk.
func verifyEncrypted(dir, runTag string) error {
	needle := []byte(runTag)
	checked := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == ".metrics-key" || name == "flock.lock" {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		magic := make([]byte, 8)
		if _, err := io.ReadFull(f, magic); err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if !bytes.Equal(magic, []byte("MTXAEAD1")) {
			return fmt.Errorf("file %s is not encrypted", path)
		}
		leaked, err := streamContains(path, needle)
		if err != nil {
			return err
		}
		if leaked {
			return fmt.Errorf("plaintext run tag leaked on disk: %s", path)
		}
		checked++
		return nil
	})
	if err != nil {
		return err
	}
	if checked == 0 {
		return fmt.Errorf("no encrypted storage files were found under %s", dir)
	}
	fmt.Printf("encryption check: %d files carry MTXAEAD1 envelopes, no plaintext leak\n", checked)
	return nil
}

func streamContains(path string, needle []byte) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	const chunk = 1 << 20
	buf := make([]byte, 0, chunk+len(needle))
	tmp := make([]byte, chunk)
	for {
		n, err := f.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if bytes.Contains(buf, needle) {
				return true, nil
			}
			if len(buf) > len(needle) {
				buf = append([]byte(nil), buf[len(buf)-len(needle):]...)
			}
		}
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("scan %s: %w", path, err)
		}
	}
}

// verifyReopen proves the data survives a close/open cycle with the same key.
func verifyReopen(dir string, key []byte, days int, runTag string, written uint64) error {
	db, err := metrics.Unlock(dir, key, metrics.WithRetentionDays(days))
	if err != nil {
		return fmt.Errorf("reopen encrypted database: %w", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	series, err := db.Select(ctx, metrics.Selector{Labels: map[string]string{"run_id": runTag}}, metrics.TimeRange{}, 100000)
	if err != nil {
		return fmt.Errorf("reopen select: %w", err)
	}
	var points int
	for _, s := range series {
		points += len(s.Points)
	}
	if uint64(points) != written {
		return fmt.Errorf("reopen select for %q: got %d points in %d series, want %d", runTag, points, len(series), written)
	}
	fmt.Printf("reopen check: found %d points in %d series for run %s after close/open with key\n", points, len(series), runTag)
	return nil
}

func dirSize(path string) (int64, error) {
	var total int64
	err := filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			total += fi.Size()
		}
		return nil
	})
	return total, err
}

func percentile(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	v := append([]time.Duration(nil), d...)
	slices.Sort(v)
	return v[int(float64(len(v)-1)*p+0.5)]
}

func max1(f float64) float64 {
	if f < 1e-9 {
		return 1e-9
	}
	return f
}
