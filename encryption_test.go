package metrics

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcgauthier/logmetrics/internal/cryptofs"
)

func TestEncryptedDatabaseRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "encrypted-metrics")
	key := bytes.Repeat([]byte{0x5a}, 32)
	db, err := Unlock(root, key, WithRetentionDays(7))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	records := make([]Sample, 0, 2500)
	for i := 0; i < 2500; i++ {
		records = append(records, Sample{
			Metric: "sensitive_metric_secret_string_sentinel",
			Labels: map[string]string{
				"job": "api", "host": "private-host-01",
			},
			Time:  base.Add(time.Duration(i) * time.Second),
			Value: float64(i),
		})
	}
	if err := db.WriteBatch(records); err != nil {
		t.Fatal(err)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Every file except the key marker and the lock must carry the
	// encryption envelope, and no series bytes may leak anywhere.
	envelopeCount := 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == ".metrics-key" || d.Name() == "flock.lock" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.HasPrefix(b, []byte("MTXAEAD1")) {
			t.Errorf("file %s is not encrypted", path)
		}
		if bytes.Contains(b, []byte("secret_string_sentinel")) || bytes.Contains(b, []byte("private-host-01")) {
			t.Errorf("plaintext leaked on disk: %s", path)
		}
		envelopeCount++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if envelopeCount == 0 {
		t.Fatal("no encrypted storage files were found")
	}
	if _, err := Open(root); !errors.Is(err, cryptofs.ErrLocked) {
		t.Fatalf("unkeyed open returned %v", err)
	}
	if _, err := Unlock(root, bytes.Repeat([]byte{0x34}, 32)); !errors.Is(err, cryptofs.ErrInvalidKey) {
		t.Fatalf("wrong key returned %v", err)
	}
	db, err = Unlock(root, key, WithRetentionDays(7))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	results, err := db.Select(context.Background(), Selector{
		Metric: "sensitive_metric_secret_string_sentinel",
		Labels: map[string]string{"job": "api"},
	}, TimeRange{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || len(results[0].Points) != 2500 {
		t.Fatalf("got %d series, want 1 with 2500 points", len(results))
	}
	if results[0].Labels["host"] != "private-host-01" {
		t.Fatalf("invalid decrypted labels %+v", results[0].Labels)
	}
	if results[0].Points[0].Value != 0 || results[0].Points[2499].Value != 2499 {
		t.Fatalf("decrypted points corrupted: first=%v last=%v",
			results[0].Points[0], results[0].Points[2499])
	}
}

func TestEncryptedDatabaseRejectsPlaintext(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(Sample{Metric: "not_encrypted", Value: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Unlock(dir, bytes.Repeat([]byte{0x31}, 32))
	if !errors.Is(err, cryptofs.ErrPlaintext) {
		t.Fatalf("unexpected err: %v", err)
	}
}
