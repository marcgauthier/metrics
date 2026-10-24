// Command limits proves large but in-range values round-trip: a 60000-byte
// label value, a series identity at exactly the 65472-byte bound, and a
// sample carrying 100 labels. Past the bound, writes are rejected instead
// of being silently dropped by the engine index.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/marcgauthier/metrics"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS: limits")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-limits-*")
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
	big := strings.Repeat("0123456789abcdef", 3750)
	if err := db.Write(metrics.Sample{
		Metric: "limits_marker_big",
		Labels: map[string]string{"blob": big},
		Value:  1,
	}); err != nil {
		return fmt.Errorf("write 60000-byte label value: %w", err)
	}
	// Exactly at the bound: metric + key + value == 65472.
	edgeValue := strings.Repeat("e", 65472-len("limits_marker_edge")-len("blob"))
	if err := db.Write(metrics.Sample{
		Metric: "limits_marker_edge",
		Labels: map[string]string{"blob": edgeValue},
		Value:  2,
	}); err != nil {
		return fmt.Errorf("write boundary identity: %w", err)
	}
	if err := db.Write(metrics.Sample{
		Metric: "limits_marker_edge",
		Labels: map[string]string{"blob": edgeValue + "x"},
		Value:  3,
	}); err == nil {
		return fmt.Errorf("identity one byte past the bound: got nil error")
	}
	wide := map[string]string{}
	for i := 0; i < 100; i++ {
		wide[fmt.Sprintf("label_%03d", i)] = fmt.Sprintf("value-%d", i)
	}
	if err := db.Write(metrics.Sample{
		Metric: "limits_marker_wide",
		Labels: wide,
		Time:   time.Now().Add(-time.Second),
		Value:  4,
	}); err != nil {
		return fmt.Errorf("write 100-label sample: %w", err)
	}
	if err := db.Write(metrics.Sample{Metric: "m", Labels: map[string]string{"k": strings.Repeat("v", 65536)}, Value: 1}); err == nil {
		return fmt.Errorf("65536-byte label value: got nil error")
	}
	if err := db.Write(metrics.Sample{Metric: strings.Repeat("n", 65536), Value: 1}); err == nil {
		return fmt.Errorf("65536-byte metric name: got nil error")
	}
	if err := db.Flush(); err != nil {
		return err
	}
	series, err := db.Select(ctx, metrics.Selector{Metric: "limits_marker_big"}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 1 || series[0].Labels["blob"] != big {
		return fmt.Errorf("big label value did not round-trip exactly")
	}
	series, err = db.Select(ctx, metrics.Selector{Metric: "limits_marker_edge"}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 1 || series[0].Labels["blob"] != edgeValue {
		return fmt.Errorf("boundary identity did not round-trip exactly")
	}
	series, err = db.Select(ctx, metrics.Selector{Metric: "limits_marker_wide"}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 1 || len(series[0].Labels) != 100 {
		return fmt.Errorf("100-label sample: got %+v", series)
	}
	if series[0].Labels["label_042"] != "value-42" {
		return fmt.Errorf("label_042: got %q", series[0].Labels["label_042"])
	}
	return nil
}
