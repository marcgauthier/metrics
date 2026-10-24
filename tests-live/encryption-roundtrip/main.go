// Command encryption-roundtrip proves Unlock/Open with a key, reopening,
// wrong-key and missing-key rejection, and that a plaintext database stays
// usable without a key. It uses only the public API.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marcgauthier/metrics"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS: encryption-roundtrip")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-encryption-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	key := bytes.Repeat([]byte{0x5a}, 32)
	db, err := metrics.Unlock(dir, key, metrics.WithRetentionDays(7))
	if err != nil {
		return err
	}
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	const n = 500
	for i := 0; i < n; i++ {
		if err := db.Write(metrics.Sample{
			Metric: "sensitive_metric_live_sentinel",
			Labels: map[string]string{"host": "private-live-host-01"},
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
	envelopes := 0
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if d.Name() == ".metrics-key" || d.Name() == "flock.lock" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.HasPrefix(b, []byte("MTXAEAD1")) {
			return fmt.Errorf("file %s is not encrypted", path)
		}
		if bytes.Contains(b, []byte("live_sentinel")) || bytes.Contains(b, []byte("private-live-host")) {
			return fmt.Errorf("plaintext leaked on disk: %s", path)
		}
		envelopes++
		return nil
	})
	if err != nil {
		return err
	}
	if envelopes == 0 {
		return fmt.Errorf("no encrypted storage files were found")
	}
	if _, err := metrics.Open(dir); err == nil {
		return fmt.Errorf("unkeyed open: got nil error")
	} else if !strings.Contains(err.Error(), "32-byte") {
		return fmt.Errorf("unkeyed open: unexpected error %v", err)
	}
	if _, err := metrics.Unlock(dir, bytes.Repeat([]byte{0x34}, 32)); err == nil {
		return fmt.Errorf("wrong key: got nil error")
	} else if !strings.Contains(err.Error(), "invalid encryption key") {
		return fmt.Errorf("wrong key: unexpected error %v", err)
	}
	db, err = metrics.Unlock(dir, key, metrics.WithRetentionDays(7))
	if err != nil {
		return err
	}
	series, err := db.Select(context.Background(), metrics.Selector{Metric: "sensitive_metric_live_sentinel"}, metrics.TimeRange{}, 10)
	if err != nil {
		db.Close()
		return err
	}
	if len(series) != 1 || len(series[0].Points) != n {
		db.Close()
		return fmt.Errorf("reopen: got %+v", series)
	}
	if series[0].Labels["host"] != "private-live-host-01" {
		db.Close()
		return fmt.Errorf("labels: got %v", series[0].Labels)
	}
	if err := db.Close(); err != nil {
		return err
	}
	return checkPlaintextUnaffected()
}

// checkPlaintextUnaffected proves databases created without a key keep
// working without one.
func checkPlaintextUnaffected() error {
	dir, err := os.MkdirTemp("", "metrics-live-plaintext-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := metrics.Open(dir)
	if err != nil {
		return err
	}
	if err := db.Write(metrics.Sample{Metric: "plain_marker", Value: 1}); err != nil {
		db.Close()
		return err
	}
	if err := db.Flush(); err != nil {
		db.Close()
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	other, err := metrics.Open(dir)
	if err != nil {
		return err
	}
	defer other.Close()
	series, err := other.Select(context.Background(), metrics.Selector{Metric: "plain_marker"}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 1 {
		return fmt.Errorf("plaintext reopen: got %+v", series)
	}
	return nil
}
