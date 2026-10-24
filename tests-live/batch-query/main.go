// Command batch-query proves WriteBatch across the internal chunk boundary,
// streaming Query, and that the series cap fails loudly instead of
// truncating.
package main

import (
	"context"
	"errors"
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
	fmt.Println("PASS: batch-query")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-batch-query-*")
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
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-5 * time.Second)
	const n = 600
	samples := make([]metrics.Sample, 0, n)
	for i := 0; i < n; i++ {
		job := "a"
		if i%2 == 1 {
			job = "b"
		}
		samples = append(samples, metrics.Sample{
			Metric: "batch_marker",
			Labels: map[string]string{"job": job},
			Time:   base.Add(time.Duration(i) * time.Millisecond),
			Value:  float64(i),
		})
	}
	if err := db.WriteBatch(samples); err != nil {
		return err
	}
	if err := db.Flush(); err != nil {
		return err
	}
	var count, points int
	err = db.Query(ctx, metrics.Selector{Metric: "batch_marker"}, metrics.TimeRange{},
		func(s metrics.Series) error {
			count++
			points += len(s.Points)
			return nil
		})
	if err != nil {
		return err
	}
	if count != 2 {
		return fmt.Errorf("series=%d, want 2", count)
	}
	if points != n {
		return fmt.Errorf("points=%d, want %d", points, n)
	}
	if _, err := db.Select(ctx, metrics.Selector{Metric: "batch_marker"}, metrics.TimeRange{}, 1); !errors.Is(err, metrics.ErrTooManySeries) {
		return fmt.Errorf("cap 1: got %v, want ErrTooManySeries", err)
	}
	series, err := db.Select(ctx, metrics.Selector{Metric: "batch_marker"}, metrics.TimeRange{}, 2)
	if err != nil {
		return err
	}
	if len(series) != 2 {
		return fmt.Errorf("cap 2: got %d series, want 2", len(series))
	}
	return nil
}
