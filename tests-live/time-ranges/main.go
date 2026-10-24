// Command time-ranges proves time-range selection: full, sub-range, open
// start, open end, empty matches, reversed ranges, and Latest.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/marcgauthier/metrics"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS: time-ranges")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-time-ranges-*")
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
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-10 * time.Minute)
	for i := 0; i < 5; i++ {
		if err := db.Write(metrics.Sample{
			Metric: "range_marker",
			Time:   base.Add(time.Duration(i) * time.Minute),
			Value:  float64(i),
		}); err != nil {
			return err
		}
	}
	if err := db.Flush(); err != nil {
		return err
	}
	values := func(tr metrics.TimeRange) ([]float64, error) {
		series, err := db.Select(ctx, metrics.Selector{Metric: "range_marker"}, tr, 10)
		if err != nil {
			return nil, err
		}
		if len(series) != 1 {
			return nil, fmt.Errorf("got %d series, want 1", len(series))
		}
		var out []float64
		for _, p := range series[0].Points {
			out = append(out, p.Value)
		}
		return out, nil
	}
	got, err := values(metrics.TimeRange{})
	if err != nil {
		return fmt.Errorf("open range: %w", err)
	}
	if fmt.Sprint(got) != "[0 1 2 3 4]" {
		return fmt.Errorf("open range values=%v", got)
	}
	got, err = values(metrics.TimeRange{Start: base.Add(time.Minute), End: base.Add(3 * time.Minute)})
	if err != nil {
		return fmt.Errorf("sub range: %w", err)
	}
	if fmt.Sprint(got) != "[1 2 3]" {
		return fmt.Errorf("sub range values=%v", got)
	}
	got, err = values(metrics.TimeRange{Start: base.Add(3 * time.Minute)})
	if err != nil {
		return fmt.Errorf("open end: %w", err)
	}
	if fmt.Sprint(got) != "[3 4]" {
		return fmt.Errorf("open end values=%v", got)
	}
	got, err = values(metrics.TimeRange{End: base.Add(time.Minute)})
	if err != nil {
		return fmt.Errorf("open start: %w", err)
	}
	if fmt.Sprint(got) != "[0 1]" {
		return fmt.Errorf("open start values=%v", got)
	}
	empty, err := db.Select(ctx, metrics.Selector{Metric: "range_marker"}, metrics.TimeRange{
		Start: base.Add(time.Hour), End: base.Add(2 * time.Hour),
	}, 10)
	if err != nil {
		return fmt.Errorf("empty range: %w", err)
	}
	if len(empty) != 0 {
		return fmt.Errorf("empty range: got %+v", empty)
	}
	if _, err := db.Select(ctx, metrics.Selector{Metric: "range_marker"}, metrics.TimeRange{
		Start: base.Add(time.Minute), End: base,
	}, 10); err == nil {
		return fmt.Errorf("reversed range: got nil error")
	}
	latest, err := db.Latest(ctx, metrics.Selector{Metric: "range_marker"}, 10)
	if err != nil {
		return err
	}
	if len(latest) != 1 || len(latest[0].Points) != 1 || latest[0].Points[0].Value != 4 {
		return fmt.Errorf("latest: got %+v", latest)
	}
	return nil
}
