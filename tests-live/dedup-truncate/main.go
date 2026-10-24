// Command dedup-truncate proves the write-coalescing contract: repeated
// writes of the same series and millisecond keep the last value, and
// sub-millisecond precision is truncated to milliseconds.
//
// Note: loggy's disk-eviction app has no equivalent here. The
// VictoriaMetrics storage engine offers no disk-usage-cap eviction, so the
// package offers no WithMaxDiskBytes option; retention plus
// WithMinFreeDiskBytes is the safeguard.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/marcgauthier/logmetrics"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS: dedup-truncate")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-dedup-truncate-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := metrics.Open(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	for _, v := range []float64{100, 200, 300} {
		if err := db.Write(metrics.Sample{Metric: "dedup_marker", Time: ts, Value: v}); err != nil {
			return err
		}
	}
	subMs := ts.Add(500 * time.Microsecond)
	if err := db.Write(metrics.Sample{Metric: "trunc_marker", Time: subMs, Value: 1}); err != nil {
		return err
	}
	if err := db.Flush(); err != nil {
		return err
	}
	series, err := db.Select(ctx, metrics.Selector{Metric: "dedup_marker"}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 1 || len(series[0].Points) != 1 {
		return fmt.Errorf("dedup: got %+v, want 1 point", series)
	}
	if series[0].Points[0].Value != 300 {
		return fmt.Errorf("dedup: value=%v, want last write wins (300)", series[0].Points[0].Value)
	}
	series, err = db.Select(ctx, metrics.Selector{Metric: "trunc_marker"}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 1 || len(series[0].Points) != 1 {
		return fmt.Errorf("truncate: got %+v", series)
	}
	if got := series[0].Points[0].Time; !got.Equal(ts) {
		return fmt.Errorf("truncate: time=%v, want %v", got, ts)
	}
	return nil
}
