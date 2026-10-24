// Command exact-match proves label values containing regexp-like
// metacharacters (dots, stars, pipes, braces, quotes, backslashes, parens)
// match exactly and literally: selecting one never matches another.
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
	fmt.Println("PASS: exact-match")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-exact-match-*")
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
	values := []string{
		".*",
		"foo|bar",
		"{a,b}",
		`"quoted"`,
		`back\slash`,
		"(group)+",
		"prefix.*suffix",
		"v1.2.3",
	}
	for i, v := range values {
		if err := db.Write(metrics.Sample{
			Metric: "exact_marker",
			Labels: map[string]string{"pattern": v},
			Time:   base.Add(time.Duration(i) * time.Second),
			Value:  float64(i),
		}); err != nil {
			return fmt.Errorf("write %q: %w", v, err)
		}
	}
	if err := db.Flush(); err != nil {
		return err
	}
	for i, v := range values {
		series, err := db.Select(ctx, metrics.Selector{
			Metric: "exact_marker",
			Labels: map[string]string{"pattern": v},
		}, metrics.TimeRange{}, 10)
		if err != nil {
			return fmt.Errorf("select %q: %w", v, err)
		}
		if len(series) != 1 || len(series[0].Points) != 1 || series[0].Points[0].Value != float64(i) {
			return fmt.Errorf("select %q: got %+v, want exactly point %d", v, series, i)
		}
	}
	// A value that is only a prefix matches nothing.
	series, err := db.Select(ctx, metrics.Selector{
		Metric: "exact_marker",
		Labels: map[string]string{"pattern": "foo"},
	}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 0 {
		return fmt.Errorf("prefix match: got %+v, want nothing", series)
	}
	return nil
}
