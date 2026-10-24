package metrics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTestDB(t *testing.T, opts ...Option) *DB {
	t.Helper()
	db, err := Open(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return db
}

func TestWriteSelectAndStats(t *testing.T) {
	db := openTestDB(t, WithRetentionDays(7))
	wantTime := time.Now().UTC().Truncate(time.Millisecond)
	err := db.Write(Sample{
		Metric: "http_requests_total",
		Labels: map[string]string{"job": "api", "instance": "edge-01"},
		Time:   wantTime,
		Value:  42.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	series, err := db.Select(context.Background(), Selector{
		Metric: "http_requests_total",
		Labels: map[string]string{"job": "api"},
	}, TimeRange{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 {
		t.Fatalf("got %d series, want 1: %#v", len(series), series)
	}
	s := series[0]
	if s.Metric != "http_requests_total" || s.Labels["instance"] != "edge-01" {
		t.Fatalf("unexpected series: %#v", s)
	}
	if len(s.Points) != 1 || s.Points[0].Value != 42.5 {
		t.Fatalf("unexpected points: %#v", s.Points)
	}
	if !s.Points[0].Time.Equal(wantTime) {
		t.Fatalf("time: got %v want %v", s.Points[0].Time, wantTime)
	}
	stats, err := db.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Series != 1 {
		t.Errorf("series: got %d, want 1: %#v", stats.Series, stats)
	}
	if stats.RowsAdded != 1 || stats.RowsReceived != 1 {
		t.Errorf("rows: got received=%d added=%d, want 1/1: %#v",
			stats.RowsReceived, stats.RowsAdded, stats)
	}
}

func TestWriteBatchAndQuery(t *testing.T) {
	db := openTestDB(t)
	// 600 samples cross the internal 512-sample ingestion chunk boundary.
	// Distinct millisecond per sample: identical (series, timestamp) pairs
	// collapse to one point.
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-2 * time.Second)
	var samples []Sample
	for i := 0; i < 600; i++ {
		job := "a"
		if i%2 == 1 {
			job = "b"
		}
		samples = append(samples, Sample{
			Metric: "batch_marker",
			Labels: map[string]string{"job": job},
			Time:   base.Add(time.Duration(i) * time.Millisecond),
			Value:  float64(i),
		})
	}
	if err := db.WriteBatch(samples); err != nil {
		t.Fatal(err)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	var count int
	var points int
	err := db.Query(context.Background(), Selector{Metric: "batch_marker"}, TimeRange{},
		func(s Series) error {
			count++
			points += len(s.Points)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("series=%d, want 2", count)
	}
	if points != 600 {
		t.Fatalf("points=%d, want 600", points)
	}
}

func TestQueryCallbackError(t *testing.T) {
	db := openTestDB(t)
	if err := db.Write(Sample{Metric: "callback_error_marker", Value: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	want := errors.New("stop visiting")
	err := db.Query(context.Background(), Selector{Metric: "callback_error_marker"}, TimeRange{},
		func(Series) error {
			return want
		})
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func TestReopenAndRecover(t *testing.T) {
	path := t.TempDir()
	db, err := Open(path, WithRetentionDays(5))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(Sample{
		Metric: "persist_marker",
		Labels: map[string]string{"run": "first"},
		Value:  7,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := Open(path, WithRetentionDays(5))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	series, err := other.Select(context.Background(), Selector{Metric: "persist_marker"}, TimeRange{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || len(series[0].Points) != 1 || series[0].Points[0].Value != 7 {
		t.Fatalf("unexpected persisted series: %#v", series)
	}
	if series[0].Labels["run"] != "first" {
		t.Fatalf("labels lost: %#v", series[0].Labels)
	}
	stats, err := other.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Series != 1 {
		t.Fatalf("series after reopen: got %d, want 1", stats.Series)
	}
	// Row counters accumulate since Open and reset on reopen.
	if stats.RowsAdded != 0 || stats.RowsReceived != 0 {
		t.Fatalf("row counters after reopen: got %+v, want zeros", stats)
	}
}

func TestConcurrentWriters(t *testing.T) {
	db := openTestDB(t)
	const writers, perWriter = 8, 25
	// Base the timestamps in the past: a zero End selects up to the current
	// time, so future-dated samples would be excluded from the result.
	now := time.Now().UTC().Truncate(time.Millisecond).Add(-2 * time.Second)
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for id := 0; id < writers; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				// Distinct millisecond per sample so every write is a
				// distinct point even though the series is shared.
				ts := now.Add(time.Duration(id*perWriter+i) * time.Millisecond)
				if err := db.Write(Sample{
					Metric: "parallel_marker",
					Labels: map[string]string{"writer": fmt.Sprint(id)},
					Time:   ts,
					Value:  float64(i),
				}); err != nil {
					errs <- err
					return
				}
			}
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	series, err := db.Select(context.Background(), Selector{Metric: "parallel_marker"}, TimeRange{}, writers+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != writers {
		t.Fatalf("got %d series, want %d", len(series), writers)
	}
	var points int
	for _, s := range series {
		points += len(s.Points)
	}
	if points != writers*perWriter {
		t.Fatalf("got %d points, want %d", points, writers*perWriter)
	}
}

func TestInputValidation(t *testing.T) {
	db := openTestDB(t, WithRetentionDays(1))
	if !errors.Is(db.Write(Sample{
		Metric: "m", Time: time.Now().Add(-48 * time.Hour), Value: 1,
	}), ErrExpiredTimestamp) {
		t.Fatal("expected expired timestamp")
	}
	if !errors.Is(db.Write(Sample{
		Metric: "m", Time: time.Now().Add(48 * time.Hour), Value: 1,
	}), ErrFutureTimestamp) {
		t.Fatal("expected future timestamp")
	}
	for _, sample := range []Sample{
		{Metric: "", Value: 1},
		{Metric: "  ", Value: 1},
		{Metric: "__reserved", Value: 1},
		{Metric: "m", Labels: map[string]string{"": "v"}, Value: 1},
		{Metric: "m", Labels: map[string]string{"__name__": "v"}, Value: 1},
		{Metric: "m", Labels: map[string]string{"__x": "v"}, Value: 1},
		{Metric: "m", Labels: map[string]string{"k": ""}, Value: 1},
		{Metric: "m", Value: math.NaN()},
		{Metric: "m", Value: math.Inf(1)},
		{Metric: "m", Value: math.Inf(-1)},
	} {
		if err := db.Write(sample); err == nil {
			t.Fatalf("expected rejection of %+v", sample)
		}
	}
	// A rejected batch member rejects the whole batch before ingestion.
	err := db.WriteBatch([]Sample{
		{Metric: "batch_atomic_marker", Value: 1},
		{Metric: "", Value: 2},
	})
	if err == nil {
		t.Fatal("expected batch rejection")
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	series, err := db.Select(context.Background(), Selector{Metric: "batch_atomic_marker"}, TimeRange{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 0 {
		t.Fatalf("rejected batch was partially ingested: %#v", series)
	}
	ctx := context.Background()
	if _, err := db.Select(ctx, Selector{}, TimeRange{}, 0); err == nil {
		t.Fatal("expected invalid maxSeries error")
	}
	if _, err := db.Select(ctx, Selector{Metric: "__x"}, TimeRange{}, 10); err == nil {
		t.Fatal("expected invalid selector metric error")
	}
	if _, err := db.Select(ctx, Selector{Labels: map[string]string{"k": ""}}, TimeRange{}, 10); err == nil {
		t.Fatal("expected invalid selector label error")
	}
	tr := TimeRange{Start: time.Now(), End: time.Now().Add(-time.Hour)}
	if _, err := db.Select(ctx, Selector{}, tr, 10); err == nil {
		t.Fatal("expected reversed time range error")
	}
	if err := db.Query(ctx, Selector{}, TimeRange{}, nil); err == nil {
		t.Fatal("expected missing callback error")
	}
	if _, err := db.Select(nil, Selector{}, TimeRange{}, 10); err == nil {
		t.Fatal("expected nil context error")
	}
	if _, err := db.Latest(ctx, Selector{}, 0); err == nil {
		t.Fatal("expected invalid latest maxSeries error")
	}
}

func TestInvalidOptions(t *testing.T) {
	if _, err := Open(t.TempDir(), WithRetentionDays(0)); err == nil {
		t.Fatal("expected retention validation failure")
	}
	if _, err := Open(t.TempDir(), WithRetentionDays(36501)); err == nil {
		t.Fatal("expected retention validation failure")
	}
	if _, err := Open(t.TempDir(), WithFlushInterval(time.Millisecond)); err == nil {
		t.Fatal("expected flush interval validation failure")
	}
	if _, err := Open(t.TempDir(), WithMinFreeDiskBytes(-1)); err == nil {
		t.Fatal("expected min free disk validation failure")
	}
	if _, err := Open(t.TempDir(), WithEncryptionKey([]byte("short"))); err == nil {
		t.Fatal("expected encryption key validation failure")
	}
	if _, err := Open(t.TempDir(), nil); err == nil {
		t.Fatal("expected nil option failure")
	}
	if _, err := Open("  "); err == nil {
		t.Fatal("expected empty directory failure")
	}
}

func TestTooManySeries(t *testing.T) {
	db := openTestDB(t)
	for _, m := range []string{"cap_a", "cap_b", "cap_c"} {
		if err := db.Write(Sample{Metric: m, Value: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Select(context.Background(), Selector{}, TimeRange{}, 2); !errors.Is(err, ErrTooManySeries) {
		t.Fatalf("got %v, want ErrTooManySeries", err)
	}
	series, err := db.Select(context.Background(), Selector{}, TimeRange{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 3 {
		t.Fatalf("got %d series, want 3", len(series))
	}
}

func TestLatest(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().UTC().Truncate(time.Millisecond).Add(-3 * time.Second)
	for i, v := range []float64{10, 20, 30} {
		if err := db.Write(Sample{
			Metric: "latest_marker",
			Time:   now.Add(time.Duration(i) * time.Second),
			Value:  v,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Write(Sample{Metric: "latest_other", Time: now, Value: 5}); err != nil {
		t.Fatal(err)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	series, err := db.Latest(context.Background(), Selector{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 {
		t.Fatalf("got %d series, want 2: %#v", len(series), series)
	}
	for _, s := range series {
		if len(s.Points) != 1 {
			t.Fatalf("series %s has %d points, want exactly the latest one", s.Metric, len(s.Points))
		}
	}
	if series[0].Metric != "latest_marker" || series[0].Points[0].Value != 30 {
		t.Fatalf("wrong latest point: %#v", series[0])
	}
}

func TestDuplicateTimestampKeepsLast(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := db.WriteBatch([]Sample{
		{Metric: "dup_marker", Time: now, Value: 100},
		{Metric: "dup_marker", Time: now, Value: 200},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	series, err := db.Select(context.Background(), Selector{Metric: "dup_marker"}, TimeRange{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || len(series[0].Points) != 1 {
		t.Fatalf("unexpected series: %#v", series)
	}
	if series[0].Points[0].Value != 200 {
		t.Fatalf("value=%v, want last write wins (200)", series[0].Points[0].Value)
	}
}

func TestSubMillisecondTruncation(t *testing.T) {
	db := openTestDB(t)
	ts := time.Now().UTC().Truncate(time.Millisecond).Add(500 * time.Microsecond)
	if err := db.Write(Sample{Metric: "trunc_marker", Time: ts, Value: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	series, err := db.Select(context.Background(), Selector{Metric: "trunc_marker"}, TimeRange{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || len(series[0].Points) != 1 {
		t.Fatalf("unexpected series: %#v", series)
	}
	if got := series[0].Points[0].Time; !got.Equal(ts.Truncate(time.Millisecond)) {
		t.Fatalf("time=%v, want truncated %v", got, ts.Truncate(time.Millisecond))
	}
}

func TestTimeRangeFiltering(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := 0; i < 5; i++ {
		if err := db.Write(Sample{
			Metric: "range_marker",
			Time:   now.Add(time.Duration(i) * time.Minute),
			Value:  float64(i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	series, err := db.Select(context.Background(), Selector{Metric: "range_marker"}, TimeRange{
		Start: now.Add(time.Minute),
		End:   now.Add(3 * time.Minute),
	}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 {
		t.Fatalf("got %d series, want 1", len(series))
	}
	var values []float64
	for _, p := range series[0].Points {
		values = append(values, p.Value)
	}
	if len(values) != 3 || values[0] != 1 || values[1] != 2 || values[2] != 3 {
		t.Fatalf("values=%v, want [1 2 3]", values)
	}
}

func TestEmptySelectorMatchesAll(t *testing.T) {
	db := openTestDB(t)
	for _, m := range []string{"all_a", "all_b"} {
		if err := db.Write(Sample{Metric: m, Labels: map[string]string{"k": "v"}, Value: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	series, err := db.Select(context.Background(), Selector{}, TimeRange{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 {
		t.Fatalf("got %d series, want 2", len(series))
	}
	if series[0].Metric != "all_a" || series[1].Metric != "all_b" {
		t.Fatalf("series not ordered by metric name: %#v", series)
	}
}

func TestSelectOrderingAndPointOrder(t *testing.T) {
	db := openTestDB(t)
	now := time.Now().UTC().Truncate(time.Millisecond).Add(-10 * time.Second)
	// Write points out of order across two series.
	samples := []Sample{
		{Metric: "order_b", Time: now.Add(2 * time.Second), Value: 2},
		{Metric: "order_a", Labels: map[string]string{"z": "1"}, Time: now.Add(3 * time.Second), Value: 3},
		{Metric: "order_b", Time: now, Value: 0},
		{Metric: "order_a", Labels: map[string]string{"z": "1"}, Time: now, Value: 0},
		{Metric: "order_a", Labels: map[string]string{"a": "1"}, Time: now, Value: 9},
	}
	if err := db.WriteBatch(samples); err != nil {
		t.Fatal(err)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	series, err := db.Select(context.Background(), Selector{}, TimeRange{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 3 {
		t.Fatalf("got %d series, want 3", len(series))
	}
	// order_a/a=1, order_a/z=1, order_b.
	if series[0].Metric != "order_a" || series[0].Labels["a"] != "1" {
		t.Fatalf("series[0] wrong: %#v", series[0])
	}
	if series[1].Metric != "order_a" || series[1].Labels["z"] != "1" {
		t.Fatalf("series[1] wrong: %#v", series[1])
	}
	if series[2].Metric != "order_b" {
		t.Fatalf("series[2] wrong: %#v", series[2])
	}
	for _, s := range series {
		for i := 1; i < len(s.Points); i++ {
			if !s.Points[i-1].Time.Before(s.Points[i].Time) {
				t.Fatalf("series %s points out of order: %#v", s.Metric, s.Points)
			}
		}
	}
}

func TestSeriesIdentityBound(t *testing.T) {
	db := openTestDB(t)
	metric := "bound_marker"
	key := "blob"
	edge := strings.Repeat("e", maxSeriesIdentityBytes-len(metric)-len(key))
	if err := db.Write(Sample{Metric: metric, Labels: map[string]string{key: edge}, Value: 1}); err != nil {
		t.Fatalf("boundary identity: %v", err)
	}
	if err := db.Write(Sample{Metric: metric, Labels: map[string]string{key: edge + "x"}, Value: 2}); err == nil {
		t.Fatal("one byte past the bound: got nil error")
	}
	if err := db.Write(Sample{Metric: strings.Repeat("n", maxSeriesIdentityBytes+1), Value: 3}); err == nil {
		t.Fatal("oversized bare metric: got nil error")
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	series, err := db.Select(context.Background(), Selector{Metric: metric}, TimeRange{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || series[0].Labels[key] != edge {
		t.Fatalf("boundary identity did not round-trip: %+v", series)
	}
}

func TestCloseIdempotent(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.Write(Sample{Metric: "m", Value: 1}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Write after close: got %v", err)
	}
	if _, err := db.Select(ctx, Selector{}, TimeRange{}, 10); !errors.Is(err, ErrClosed) {
		t.Fatalf("Select after close: got %v", err)
	}
	if err := db.Query(ctx, Selector{}, TimeRange{}, func(Series) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("Query after close: got %v", err)
	}
	if _, err := db.Latest(ctx, Selector{}, 10); !errors.Is(err, ErrClosed) {
		t.Fatalf("Latest after close: got %v", err)
	}
	if _, err := db.Stats(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Stats after close: got %v", err)
	}
	if err := db.Flush(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Flush after close: got %v", err)
	}
}
