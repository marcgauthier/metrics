// Command multi-day proves samples backfilled across several days land,
// stay selectable per day and as a whole, and are reflected in Stats. One
// day is 40 days back, which always crosses a month boundary and therefore
// a storage partition.
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
	fmt.Println("PASS: multi-day")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-multi-day-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := metrics.Open(dir, metrics.WithRetentionDays(60))
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	dayOffsets := []int{0, 1, 2, 3, 40}
	const perDay = 20
	for _, d := range dayOffsets {
		day := now.AddDate(0, 0, -d).Truncate(24 * time.Hour)
		samples := make([]metrics.Sample, 0, perDay)
		for i := 0; i < perDay; i++ {
			samples = append(samples, metrics.Sample{
				Metric: "multiday_marker",
				Labels: map[string]string{"day": fmt.Sprintf("d%d", d)},
				Time:   day.Add(time.Duration(i) * time.Second),
				Value:  float64(i),
			})
		}
		if err := db.WriteBatch(samples); err != nil {
			return fmt.Errorf("day %d: %w", d, err)
		}
	}
	if err := db.Flush(); err != nil {
		return err
	}
	for _, d := range dayOffsets {
		day := now.AddDate(0, 0, -d).Truncate(24 * time.Hour)
		series, err := db.Select(ctx, metrics.Selector{Metric: "multiday_marker"}, metrics.TimeRange{
			Start: day, End: day.Add(time.Hour),
		}, 100)
		if err != nil {
			return err
		}
		if len(series) != 1 || len(series[0].Points) != perDay {
			return fmt.Errorf("day %d: got %+v, want 1 series with %d points", d, series, perDay)
		}
	}
	var total int
	if err := db.Query(ctx, metrics.Selector{Metric: "multiday_marker"}, metrics.TimeRange{},
		func(s metrics.Series) error {
			total += len(s.Points)
			return nil
		}); err != nil {
		return err
	}
	if total != len(dayOffsets)*perDay {
		return fmt.Errorf("cross-day total=%d, want %d", total, len(dayOffsets)*perDay)
	}
	stats, err := db.Stats()
	if err != nil {
		return err
	}
	fmt.Printf("rows=%d series=%d\n", stats.RowsAdded, stats.Series)
	if stats.RowsAdded != uint64(len(dayOffsets)*perDay) {
		return fmt.Errorf("stats rows=%d, want %d", stats.RowsAdded, len(dayOffsets)*perDay)
	}
	if stats.Series != uint64(len(dayOffsets)) {
		return fmt.Errorf("stats series=%d, want %d", stats.Series, len(dayOffsets))
	}
	return nil
}
