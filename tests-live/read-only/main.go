// Command read-only proves the low-disk safeguard: with an unsatisfiable
// free-space requirement the database reports read-only, refuses writes
// with ErrReadOnly, and still serves reads.
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
	fmt.Println("PASS: read-only")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-read-only-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := metrics.Open(dir, metrics.WithMinFreeDiskBytes(1<<62))
	if err != nil {
		return err
	}
	defer db.Close()
	stats, err := db.Stats()
	if err != nil {
		return err
	}
	if !stats.ReadOnly {
		return fmt.Errorf("stats: got %+v, want ReadOnly", stats)
	}
	if err := db.Write(metrics.Sample{Metric: "m", Value: 1}); !errors.Is(err, metrics.ErrReadOnly) {
		return fmt.Errorf("write: got %v, want ErrReadOnly", err)
	}
	if err := db.WriteBatch([]metrics.Sample{{Metric: "m", Time: time.Now(), Value: 1}}); !errors.Is(err, metrics.ErrReadOnly) {
		return fmt.Errorf("batch: got %v, want ErrReadOnly", err)
	}
	series, err := db.Select(context.Background(), metrics.Selector{}, metrics.TimeRange{}, 10)
	if err != nil {
		return fmt.Errorf("select on read-only db: %w", err)
	}
	if len(series) != 0 {
		return fmt.Errorf("select on read-only db: got %+v", series)
	}
	return nil
}
