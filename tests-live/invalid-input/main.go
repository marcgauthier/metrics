// Command invalid-input proves every documented validation error: bad
// options, bad metric and label names, non-finite values, batch atomicity,
// bad time ranges, a nil callback, a failing callback, nil and cancelled
// contexts.
package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/marcgauthier/logmetrics"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS: invalid-input")
}

func wantErr(name string, err error) error {
	if err == nil {
		return fmt.Errorf("%s: got nil error", name)
	}
	return nil
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-invalid-input-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := wantErr("empty dir", mustOpenErr("", nil)); err != nil {
		return err
	}
	badOptions := []struct {
		name string
		opt  metrics.Option
	}{
		{"nil option", nil},
		{"retention zero", metrics.WithRetentionDays(0)},
		{"retention huge", metrics.WithRetentionDays(36501)},
		{"flush too small", metrics.WithFlushInterval(500 * time.Millisecond)},
		{"negative min free", metrics.WithMinFreeDiskBytes(-1)},
		{"short encryption key", metrics.WithEncryptionKey(make([]byte, 16))},
		{"long encryption key", metrics.WithEncryptionKey(make([]byte, 64))},
	}
	for _, tc := range badOptions {
		if err := wantErr(tc.name, mustOpenErr(dir, tc.opt)); err != nil {
			return err
		}
	}
	db, err := metrics.Open(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	badSamples := []struct {
		name   string
		sample metrics.Sample
	}{
		{"empty metric", metrics.Sample{Metric: "", Value: 1}},
		{"reserved metric", metrics.Sample{Metric: "__x", Value: 1}},
		{"empty label", metrics.Sample{Metric: "m", Labels: map[string]string{"": "v"}, Value: 1}},
		{"reserved label", metrics.Sample{Metric: "m", Labels: map[string]string{"__name__": "v"}, Value: 1}},
		{"empty value", metrics.Sample{Metric: "m", Labels: map[string]string{"k": ""}, Value: 1}},
		{"NaN", metrics.Sample{Metric: "m", Value: math.NaN()}},
		{"+Inf", metrics.Sample{Metric: "m", Value: math.Inf(1)}},
		{"-Inf", metrics.Sample{Metric: "m", Value: math.Inf(-1)}},
	}
	for _, tc := range badSamples {
		if err := wantErr(tc.name, db.Write(tc.sample)); err != nil {
			return err
		}
	}
	if _, err := db.Select(ctx, metrics.Selector{}, metrics.TimeRange{}, 0); err == nil {
		return fmt.Errorf("zero cap: got nil error")
	}
	if _, err := db.Select(ctx, metrics.Selector{}, metrics.TimeRange{}, -1); err == nil {
		return fmt.Errorf("negative cap: got nil error")
	}
	if _, err := db.Select(ctx, metrics.Selector{Metric: "__x"}, metrics.TimeRange{}, 10); err == nil {
		return fmt.Errorf("reserved selector metric: got nil error")
	}
	if _, err := db.Select(ctx, metrics.Selector{Labels: map[string]string{"k": ""}}, metrics.TimeRange{}, 10); err == nil {
		return fmt.Errorf("empty selector value: got nil error")
	}
	now := time.Now()
	if _, err := db.Select(ctx, metrics.Selector{}, metrics.TimeRange{Start: now, End: now.Add(-time.Hour)}, 10); err == nil {
		return fmt.Errorf("reversed range: got nil error")
	}
	if err := db.Query(ctx, metrics.Selector{}, metrics.TimeRange{}, nil); err == nil {
		return fmt.Errorf("nil callback: got nil error")
	}
	if _, err := db.Select(nil, metrics.Selector{}, metrics.TimeRange{}, 10); err == nil {
		return fmt.Errorf("nil context: got nil error")
	}
	if err := db.Write(metrics.Sample{Metric: "callback_marker", Value: 1}); err != nil {
		return err
	}
	if err := db.Flush(); err != nil {
		return err
	}
	sentinel := errors.New("stop visiting")
	if err := db.Query(ctx, metrics.Selector{Metric: "callback_marker"}, metrics.TimeRange{},
		func(metrics.Series) error { return sentinel }); !errors.Is(err, sentinel) {
		return fmt.Errorf("callback error: got %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.Select(cancelled, metrics.Selector{Metric: "callback_marker"}, metrics.TimeRange{}, 10); !errors.Is(err, context.Canceled) {
		return fmt.Errorf("cancelled select with data: got %v", err)
	}
	if _, err := db.Select(cancelled, metrics.Selector{Metric: "nothing_matches_this"}, metrics.TimeRange{}, 10); !errors.Is(err, context.Canceled) {
		return fmt.Errorf("cancelled select without data: got %v", err)
	}
	if err := db.Query(cancelled, metrics.Selector{}, metrics.TimeRange{}, func(metrics.Series) error { return nil }); !errors.Is(err, context.Canceled) {
		return fmt.Errorf("cancelled query: got %v", err)
	}
	return checkBatchAtomicity()
}

func mustOpenErr(dir string, opt metrics.Option) error {
	var db *metrics.DB
	var err error
	if opt == nil {
		db, err = metrics.Open(dir, nil)
	} else {
		db, err = metrics.Open(dir, opt)
	}
	if err != nil {
		return err
	}
	db.Close()
	return nil
}

// checkBatchAtomicity proves a batch containing one invalid sample ingests
// nothing at all.
func checkBatchAtomicity() error {
	dir, err := os.MkdirTemp("", "metrics-live-batch-atomic-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := metrics.Open(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	err = db.WriteBatch([]metrics.Sample{
		{Metric: "atomic_marker", Value: 1},
		{Metric: "", Value: 2},
	})
	if err == nil {
		return fmt.Errorf("batch with invalid sample: got nil error")
	}
	if err := db.Flush(); err != nil {
		return err
	}
	series, err := db.Select(context.Background(), metrics.Selector{Metric: "atomic_marker"}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 0 {
		return fmt.Errorf("rejected batch partially ingested: %+v", series)
	}
	return nil
}
