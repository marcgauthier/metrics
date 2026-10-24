// Command retention-reject proves out-of-retention and far-future samples
// are rejected with the documented sentinels while in-range samples land.
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
	fmt.Println("PASS: retention-reject")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-retention-reject-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := metrics.Open(dir, metrics.WithRetentionDays(1))
	if err != nil {
		return err
	}
	defer db.Close()
	now := time.Now()
	if !errors.Is(db.Write(metrics.Sample{Metric: "m", Time: now.Add(-25 * time.Hour), Value: 1}), metrics.ErrExpiredTimestamp) {
		return fmt.Errorf("25h old: want ErrExpiredTimestamp")
	}
	if !errors.Is(db.Write(metrics.Sample{Metric: "m", Time: now.Add(25 * time.Hour), Value: 1}), metrics.ErrFutureTimestamp) {
		return fmt.Errorf("25h future: want ErrFutureTimestamp")
	}
	if err := db.Write(metrics.Sample{Metric: "edge_marker", Time: now.Add(-23 * time.Hour), Value: 1}); err != nil {
		return fmt.Errorf("23h old: %w", err)
	}
	if err := db.Write(metrics.Sample{Metric: "edge_marker", Time: now.Add(23 * time.Hour), Value: 2}); err != nil {
		return fmt.Errorf("23h future: %w", err)
	}
	if err := db.Flush(); err != nil {
		return err
	}
	series, err := db.Select(context.Background(), metrics.Selector{Metric: "edge_marker"}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	// Only the past point is inside the default open-ended range; the
	// future point needs an explicit End.
	if len(series) != 1 || len(series[0].Points) != 1 || series[0].Points[0].Value != 1 {
		return fmt.Errorf("past edge: got %+v", series)
	}
	series, err = db.Select(context.Background(), metrics.Selector{Metric: "edge_marker"}, metrics.TimeRange{
		End: now.Add(24 * time.Hour),
	}, 10)
	if err != nil {
		return err
	}
	if len(series) != 1 || len(series[0].Points) != 2 {
		return fmt.Errorf("both edges: got %+v", series)
	}
	return nil
}
