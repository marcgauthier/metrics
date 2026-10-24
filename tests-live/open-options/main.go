// Command open-options proves every Open option combined: a nested directory
// is auto-created, writes and selects work, and the directory reopens fine
// under different options.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/marcgauthier/logmetrics"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS: open-options")
}

func run() error {
	root, err := os.MkdirTemp("", "metrics-live-open-options-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	dir := filepath.Join(root, "nested", "deep", "data")
	key := bytes.Repeat([]byte{0x3c}, 32)
	db, err := metrics.Unlock(dir, key,
		metrics.WithRetentionDays(30),
		metrics.WithFlushInterval(time.Second),
		metrics.WithMinFreeDiskBytes(1024),
	)
	if err != nil {
		return fmt.Errorf("open with all options: %w", err)
	}
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	for i := 0; i < 10; i++ {
		if err := db.Write(metrics.Sample{
			Metric: "cfg_marker",
			Labels: map[string]string{"job": "cfg"},
			Time:   base.Add(time.Duration(i) * time.Second),
			Value:  float64(i),
		}); err != nil {
			db.Close()
			return err
		}
	}
	if err := db.Flush(); err != nil {
		db.Close()
		return err
	}
	series, err := db.Select(context.Background(), metrics.Selector{Metric: "cfg_marker"}, metrics.TimeRange{}, 20)
	if err != nil {
		db.Close()
		return err
	}
	if len(series) != 1 || len(series[0].Points) != 10 {
		db.Close()
		return fmt.Errorf("got %+v, want 1 series with 10 points", series)
	}
	if err := db.Close(); err != nil {
		return err
	}
	reopened, err := metrics.Unlock(dir, key, metrics.WithRetentionDays(7))
	if err != nil {
		return fmt.Errorf("reopen with different options: %w", err)
	}
	defer reopened.Close()
	series, err = reopened.Select(context.Background(), metrics.Selector{Metric: "cfg_marker"}, metrics.TimeRange{}, 20)
	if err != nil {
		return err
	}
	if len(series) != 1 || len(series[0].Points) != 10 {
		return fmt.Errorf("after reopen: got %+v", series)
	}
	return nil
}
