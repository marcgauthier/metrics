// Command local-time proves timestamps in a non-UTC zone round-trip as the
// same instant: storage is zone-independent. Comparison is at millisecond
// precision, the engine's resolution.
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
	fmt.Println("PASS: local-time")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-local-time-*")
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
	zone := time.FixedZone("LIVE", 5*3600+1800)
	ts := time.Now().In(zone).Truncate(time.Millisecond)
	if err := db.Write(metrics.Sample{Metric: "local_time_zone_marker", Time: ts, Value: 1}); err != nil {
		return err
	}
	if err := db.Write(metrics.Sample{Metric: "local_time_utc_marker", Time: ts.UTC(), Value: 2}); err != nil {
		return err
	}
	if err := db.Flush(); err != nil {
		return err
	}
	series, err := db.Select(ctx, metrics.Selector{Metric: "local_time_zone_marker"}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 1 || len(series[0].Points) != 1 {
		return fmt.Errorf("zoned: got %+v", series)
	}
	got := series[0].Points[0].Time
	if !got.Equal(ts) || got.UnixMilli() != ts.UnixMilli() {
		return fmt.Errorf("zoned time: got %v, want %v", got, ts)
	}
	if got.Location() != time.UTC {
		return fmt.Errorf("zoned time not UTC: %v", got)
	}
	series, err = db.Select(ctx, metrics.Selector{Metric: "local_time_utc_marker"}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 1 || !series[0].Points[0].Time.Equal(ts) {
		return fmt.Errorf("utc twin: got %+v, want %v", series, ts)
	}
	return nil
}
