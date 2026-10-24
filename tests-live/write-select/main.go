// Command write-select proves the basic application flow: open a database,
// write samples with labels and explicit timestamps, flush, and find them
// with metric and label selection.
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
	fmt.Println("PASS: write-select")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-write-select-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := metrics.Open(dir, metrics.WithRetentionDays(7))
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-10 * time.Second)
	samples := []metrics.Sample{
		{Metric: "http_requests_total", Labels: map[string]string{"job": "gateway", "code": "500"}, Time: base, Value: 1},
		{Metric: "http_requests_total", Labels: map[string]string{"job": "gateway", "code": "200"}, Time: base.Add(time.Second), Value: 41},
		{Metric: "cpu_temperature", Labels: map[string]string{"host": "edge-01"}, Time: base.Add(2 * time.Second), Value: 61.5},
		{Metric: "http_requests_total", Labels: map[string]string{"job": "gateway", "code": "200"}, Value: 42},
	}
	for i, s := range samples {
		if err := db.Write(s); err != nil {
			return fmt.Errorf("write %d: %w", i, err)
		}
	}
	if err := db.Write(metrics.Sample{Metric: "bare_metric", Value: 7}); err != nil {
		return fmt.Errorf("write bare: %w", err)
	}
	if err := db.Flush(); err != nil {
		return err
	}
	series, err := db.Select(ctx, metrics.Selector{Metric: "http_requests_total"}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 2 {
		return fmt.Errorf("metric select: got %d series, want 2", len(series))
	}
	series, err = db.Select(ctx, metrics.Selector{
		Metric: "http_requests_total",
		Labels: map[string]string{"job": "gateway", "code": "500"},
	}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 1 {
		return fmt.Errorf("label select: got %d series, want 1", len(series))
	}
	s := series[0]
	if len(s.Labels) != 2 || s.Labels["code"] != "500" {
		return fmt.Errorf("labels: got %v, want exactly job/code", s.Labels)
	}
	if len(s.Points) != 1 || s.Points[0].Value != 1 {
		return fmt.Errorf("points: got %+v", s.Points)
	}
	if !s.Points[0].Time.Equal(base) {
		return fmt.Errorf("time: got %v want %v", s.Points[0].Time, base)
	}
	series, err = db.Select(ctx, metrics.Selector{Metric: "cpu_temperature"}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 1 || series[0].Points[0].Value != 61.5 {
		return fmt.Errorf("single series: got %+v", series)
	}
	series, err = db.Select(ctx, metrics.Selector{Metric: "bare_metric"}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 1 || len(series[0].Labels) != 0 {
		return fmt.Errorf("bare metric: got %+v", series)
	}
	// The zero-time sample landed at approximately now.
	series, err = db.Select(ctx, metrics.Selector{
		Metric: "http_requests_total",
		Labels: map[string]string{"code": "200"},
	}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 1 || len(series[0].Points) != 2 {
		return fmt.Errorf("zero-time series: got %+v", series)
	}
	last := series[0].Points[1]
	if last.Value != 42 || time.Since(last.Time) > 5*time.Minute {
		return fmt.Errorf("zero time not recent: %+v", last)
	}
	return nil
}
