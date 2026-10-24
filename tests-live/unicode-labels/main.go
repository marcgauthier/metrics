// Command unicode-labels proves metric names and label names/values with
// emoji, CJK, accents and quotes round-trip exactly and stay selectable.
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
	fmt.Println("PASS: unicode-labels")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-unicode-labels-*")
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
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	cases := []struct {
		metric string
		labels map[string]string
	}{
		{"requests_total", map[string]string{"route": "/café/naïve ☕"}},
		{"温度_摂氏", map[string]string{"場所": "東京", "sensor": "屋上-1"}},
		{"emoji_metric_🚀", map[string]string{"status": "ok✅", "note": `say "hi" Özil`}},
		{"accents_àéîõü", map[string]string{"légende": "crème brûlée"}},
	}
	for i, tc := range cases {
		if err := db.Write(metrics.Sample{
			Metric: tc.metric,
			Labels: tc.labels,
			Time:   base.Add(time.Duration(i) * time.Second),
			Value:  float64(i),
		}); err != nil {
			return fmt.Errorf("write %d: %w", i, err)
		}
	}
	if err := db.Flush(); err != nil {
		return err
	}
	for i, tc := range cases {
		series, err := db.Select(ctx, metrics.Selector{Metric: tc.metric}, metrics.TimeRange{}, 10)
		if err != nil {
			return fmt.Errorf("select %d: %w", i, err)
		}
		if len(series) != 1 {
			return fmt.Errorf("case %d: got %d series", i, len(series))
		}
		if series[0].Metric != tc.metric {
			return fmt.Errorf("case %d: metric %q, want %q", i, series[0].Metric, tc.metric)
		}
		for k, v := range tc.labels {
			if series[0].Labels[k] != v {
				return fmt.Errorf("case %d: labels %v, want %v", i, series[0].Labels, tc.labels)
			}
		}
		if len(series[0].Labels) != len(tc.labels) {
			return fmt.Errorf("case %d: labels %v, want %v", i, series[0].Labels, tc.labels)
		}
	}
	// A unicode label value selects exactly its own series.
	series, err := db.Select(ctx, metrics.Selector{Labels: map[string]string{"場所": "東京"}}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 1 || series[0].Metric != "温度_摂氏" {
		return fmt.Errorf("unicode label select: got %+v", series)
	}
	return nil
}
