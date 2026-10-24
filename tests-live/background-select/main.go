// Command background-select proves samples become selectable through the
// configured flush interval alone, without an explicit Flush call.
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
	fmt.Println("PASS: background-select")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-background-select-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := metrics.Open(dir, metrics.WithFlushInterval(time.Second))
	if err != nil {
		return err
	}
	defer db.Close()
	base := time.Now().UTC().Truncate(time.Millisecond)
	for i := 0; i < 5; i++ {
		if err := db.Write(metrics.Sample{
			Metric: "background_select_marker",
			Time:   base.Add(time.Duration(i) * time.Millisecond),
			Value:  float64(i),
		}); err != nil {
			return err
		}
	}
	ctx := context.Background()
	deadline := time.Now().Add(15 * time.Second)
	for {
		series, err := db.Select(ctx, metrics.Selector{Metric: "background_select_marker"}, metrics.TimeRange{}, 10)
		if err != nil {
			return err
		}
		if len(series) == 1 && len(series[0].Points) == 5 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("points not visible after 15s without Flush: %+v", series)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
