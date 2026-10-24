// Command status-panel renders an application status panel from Stats and
// proves none of the counter fields come back unknown.
package main

import (
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
	fmt.Println("PASS: status-panel")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-status-panel-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := metrics.Open(dir, metrics.WithRetentionDays(30))
	if err != nil {
		return err
	}
	defer db.Close()
	const n = 100
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-2 * time.Second)
	samples := make([]metrics.Sample, 0, n)
	for i := 0; i < n; i++ {
		samples = append(samples, metrics.Sample{
			Metric: "panel_events_total",
			Labels: map[string]string{"host": fmt.Sprintf("edge-%03d", i%8)},
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
	stats, err := db.Stats()
	if err != nil {
		return err
	}
	if stats.Series != 8 {
		return fmt.Errorf("series=%d, want 8", stats.Series)
	}
	if stats.RowsAdded != n || stats.RowsReceived != n {
		return fmt.Errorf("rows unknown: %+v", stats)
	}
	status := "Healthy"
	if stats.ReadOnly {
		status = "Read-only"
	}
	fmt.Printf("Status: %s\nTotal Series: %d\nRows Added: %d\nRows Received: %d\nDropped Too Old: %d\nDropped Too New: %d\n",
		status, stats.Series, stats.RowsAdded, stats.RowsReceived,
		stats.RowsDroppedTooOld, stats.RowsDroppedTooNew)
	return nil
}
