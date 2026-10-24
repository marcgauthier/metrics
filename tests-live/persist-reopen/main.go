// Command persist-reopen proves data and series counts survive close/open
// while process-lifetime row counters reset.
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
	fmt.Println("PASS: persist-reopen")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-persist-reopen-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := metrics.Open(dir, metrics.WithRetentionDays(5))
	if err != nil {
		return err
	}
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	for i := 0; i < 10; i++ {
		if err := db.Write(metrics.Sample{
			Metric: "persist_marker",
			Labels: map[string]string{"run": "first"},
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
	if err := db.Close(); err != nil {
		return err
	}
	other, err := metrics.Open(dir, metrics.WithRetentionDays(5))
	if err != nil {
		return err
	}
	defer other.Close()
	series, err := other.Select(context.Background(), metrics.Selector{Metric: "persist_marker"}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 1 || len(series[0].Points) != 10 {
		return fmt.Errorf("got %+v, want 1 series with 10 points", series)
	}
	if series[0].Labels["run"] != "first" || series[0].Points[9].Value != 9 {
		return fmt.Errorf("data mismatch: %+v", series[0])
	}
	stats, err := other.Stats()
	if err != nil {
		return err
	}
	if stats.Series != 1 {
		return fmt.Errorf("series=%d, want 1", stats.Series)
	}
	if stats.RowsAdded != 0 {
		return fmt.Errorf("rows added=%d after reopen, want 0 (process counter)", stats.RowsAdded)
	}
	return nil
}
