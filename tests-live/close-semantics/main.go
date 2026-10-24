// Command close-semantics proves Close is idempotent and every operation
// after Close reports ErrClosed.
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
	fmt.Println("PASS: close-semantics")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-close-semantics-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := metrics.Open(dir)
	if err != nil {
		return err
	}
	if err := db.Write(metrics.Sample{Metric: "close_marker", Value: 1}); err != nil {
		db.Close()
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("second close: %w", err)
	}
	ctx := context.Background()
	if err := db.Write(metrics.Sample{Metric: "m", Value: 1}); !errors.Is(err, metrics.ErrClosed) {
		return fmt.Errorf("Write: got %v", err)
	}
	if err := db.WriteBatch([]metrics.Sample{{Metric: "m", Time: time.Now(), Value: 1}}); !errors.Is(err, metrics.ErrClosed) {
		return fmt.Errorf("WriteBatch: got %v", err)
	}
	if _, err := db.Select(ctx, metrics.Selector{}, metrics.TimeRange{}, 10); !errors.Is(err, metrics.ErrClosed) {
		return fmt.Errorf("Select: got %v", err)
	}
	if err := db.Query(ctx, metrics.Selector{}, metrics.TimeRange{}, func(metrics.Series) error { return nil }); !errors.Is(err, metrics.ErrClosed) {
		return fmt.Errorf("Query: got %v", err)
	}
	if _, err := db.Latest(ctx, metrics.Selector{}, 10); !errors.Is(err, metrics.ErrClosed) {
		return fmt.Errorf("Latest: got %v", err)
	}
	if _, err := db.Stats(); !errors.Is(err, metrics.ErrClosed) {
		return fmt.Errorf("Stats: got %v", err)
	}
	if err := db.Flush(); !errors.Is(err, metrics.ErrClosed) {
		return fmt.Errorf("Flush: got %v", err)
	}
	return nil
}
