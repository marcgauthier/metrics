// Command empty-db proves a fresh database reports zero stats, selects
// nothing, and tolerates Flush and Close.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/marcgauthier/metrics"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS: empty-db")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-empty-db-*")
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
	stats, err := db.Stats()
	if err != nil {
		return err
	}
	if stats.Series != 0 || stats.RowsAdded != 0 || stats.RowsReceived != 0 {
		return fmt.Errorf("stats: got %+v, want zeros", stats)
	}
	series, err := db.Select(ctx, metrics.Selector{}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 0 {
		return fmt.Errorf("select: got %+v", series)
	}
	visited := false
	if err := db.Query(ctx, metrics.Selector{}, metrics.TimeRange{}, func(metrics.Series) error {
		visited = true
		return nil
	}); err != nil {
		return err
	}
	if visited {
		return fmt.Errorf("query visited a series in an empty db")
	}
	latest, err := db.Latest(ctx, metrics.Selector{}, 10)
	if err != nil {
		return err
	}
	if len(latest) != 0 {
		return fmt.Errorf("latest: got %+v", latest)
	}
	if err := db.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	return nil
}
