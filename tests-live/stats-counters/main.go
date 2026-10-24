// Command stats-counters proves Stats reports series, rows, drops and
// read-only state accurately.
package main

import (
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
	fmt.Println("PASS: stats-counters")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-stats-counters-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := metrics.Open(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	stats, err := db.Stats()
	if err != nil {
		return err
	}
	if stats.Series != 0 || stats.RowsAdded != 0 || stats.RowsReceived != 0 ||
		stats.RowsDroppedTooOld != 0 || stats.RowsDroppedTooNew != 0 || stats.ReadOnly {
		return fmt.Errorf("empty stats: got %+v, want zeros", stats)
	}
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-2 * time.Second)
	const perSeries = 100
	for i := 0; i < perSeries; i++ {
		for _, job := range []string{"a", "b"} {
			if err := db.Write(metrics.Sample{
				Metric: "stats_marker",
				Labels: map[string]string{"job": job},
				Time:   base.Add(time.Duration(i) * time.Millisecond),
				Value:  float64(i),
			}); err != nil {
				return err
			}
		}
	}
	if err := db.Flush(); err != nil {
		return err
	}
	stats, err = db.Stats()
	if err != nil {
		return err
	}
	if stats.Series != 2 {
		return fmt.Errorf("series=%d, want 2: %+v", stats.Series, stats)
	}
	if stats.RowsAdded != 2*perSeries || stats.RowsReceived != 2*perSeries {
		return fmt.Errorf("rows=%+v, want %d", stats, 2*perSeries)
	}
	if stats.RowsDroppedTooOld != 0 || stats.RowsDroppedTooNew != 0 {
		return fmt.Errorf("drops=%+v, want zeros", stats)
	}
	if stats.ReadOnly {
		return fmt.Errorf("readonly=%+v, want false", stats)
	}
	return nil
}
