package cryptofs

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedChunksAndAuthentication(t *testing.T) {
	for _, size := range []int{0, 1, 127, 64 << 10, (64 << 10) + 11, (4 << 20) + 377} {
		t.Run(string(rune('A'+size%20)), func(t *testing.T) {
			root := t.TempDir()
			key := bytes.Repeat([]byte{0x57}, 32)
			release, err := Unlock(root, key)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			path := filepath.Join(root, "data.bin")
			plain := make([]byte, size)
			if _, err := rand.Read(plain); err != nil {
				t.Fatal(err)
			}
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			w, err := NewWriter(f)
			if err != nil {
				t.Fatal(err)
			}
			for off := 0; off < len(plain); off += 1777 {
				end := min(off+1777, len(plain))
				if _, err := w.Write(plain[off:end]); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Finish(); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			onDisk, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(onDisk) > 0 && !bytes.HasPrefix(onDisk, []byte(magic)) {
				t.Fatal("not encrypted")
			}
			// A 1-byte plaintext may appear in random ciphertext by chance;
			// check a long enough sequence to avoid a flaky false positive.
			if len(plain) >= 32 && bytes.Contains(onDisk, plain[:32]) {
				t.Fatal("plaintext leaked")
			}
			r, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if r.Size() != int64(size) {
				t.Fatalf("wrong logical size: %d", r.Size())
			}
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatal("full read mismatch")
			}
			for off := 0; off < size; off += 997 {
				end := min(off+997, size)
				buf := make([]byte, end-off)
				if _, err := r.ReadAt(buf, int64(off)); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(buf, plain[off:end]) {
					t.Fatalf("random access mismatch at %d", off)
				}
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			// Every valid ciphertext must include the authenticated
			// trailer: deleting or changing it cannot produce a valid file.
			if err := os.Truncate(path, int64(len(onDisk)-1)); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path); err == nil {
				t.Fatal("truncated encrypted data was accepted")
			}
		})
	}
}

func TestReadAtEmptySlice(t *testing.T) {
	root := t.TempDir()
	key := bytes.Repeat([]byte{0x57}, 32)
	release, err := Unlock(root, key)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	path := filepath.Join(root, "data.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// io.ReaderAt requires a nil error when nothing was requested,
	// even at or beyond EOF.
	for _, off := range []int64{0, 5, 6, 100} {
		if n, err := r.ReadAt([]byte{}, off); n != 0 || err != nil {
			t.Fatalf("off=%d: got (%d, %v), want (0, nil)", off, n, err)
		}
	}
}

func FuzzWriterReaderRoundTrip(f *testing.F) {
	f.Add([]byte("hello"), 3)
	f.Add(make([]byte, 70000), 70000)
	f.Fuzz(func(t *testing.T, plain []byte, split int) {
		if len(plain) > 1<<20 || split < 0 {
			t.Skip("out of range")
		}
		root := t.TempDir()
		release, err := Unlock(root, bytes.Repeat([]byte{0x57}, 32))
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		path := filepath.Join(root, "data.bin")
		wf, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		w, err := NewWriter(wf)
		if err != nil {
			t.Fatal(err)
		}
		step := split%(64<<10) + 1
		for off := 0; off < len(plain); off += step {
			end := min(off+step, len(plain))
			if _, err := w.Write(plain[off:end]); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Finish(); err != nil {
			t.Fatal(err)
		}
		if err := wf.Close(); err != nil {
			t.Fatal(err)
		}
		r, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatal("round trip mismatch")
		}
	})
}

func TestFinishFailureIsSticky(t *testing.T) {
	root := t.TempDir()
	key := bytes.Repeat([]byte{0x57}, 32)
	release, err := Unlock(root, key)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	path := filepath.Join(root, "data.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	// Fill exactly one chunk so it flushes during Write, leaving an
	// empty buffer; then break the file so only the trailer fails.
	if _, err := w.Write(bytes.Repeat([]byte{0xab}, blockSize)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err == nil {
		t.Fatal("expected trailer write error")
	}
	// A retry must report the same failure, not a false success:
	// partial trailer bytes cannot be repaired by rewriting.
	if err := w.Finish(); err == nil {
		t.Fatal("repeat Finish after trailer failure returned nil")
	}
}

func TestProtectedNames(t *testing.T) {
	protected := []string{
		"index.bin", "values.bin", "timestamps.bin", "metaindex.bin",
		"items.bin", "lens.bin", "parts.json", "metadata.json",
		"parts.json.tmp.3", "metadata.json.tmp.41",
		"metric_usage_tracker", "metric_usage_tracker.tmp.7",
		"minTimestampForCompositeIndex", "minTimestampForCompositeIndex.tmp.2",
		"min_dedup_interval",
		"prev_hour_metric_ids_v2", "curr_hour_metric_ids_v2",
		"next_day_metric_ids_v3", "next_day_metric_ids_v3.tmp.9",
		"metaindex.bin.tmp.5",
	}
	for _, name := range protected {
		if !isProtectedName(name) {
			t.Errorf("isProtectedName(%q) = false, want true", name)
		}
	}
	unprotected := []string{
		".metrics-key", "flock.lock", "appliedRetention.txt",
		"reset_cache_on_startup", "data.0.bin.tmp", "parts.json.tmp",
		"metric_usage_tracker.tmp", "foo.tmp.bar", "snapshots",
	}
	for _, name := range unprotected {
		if isProtectedName(name) {
			t.Errorf("isProtectedName(%q) = true, want false", name)
		}
	}
}

func TestEncryptedReportsRootMembership(t *testing.T) {
	root := t.TempDir()
	key := bytes.Repeat([]byte{0x57}, 32)
	release, err := Unlock(root, key)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	inside := filepath.Join(root, "cache", "anything_at_all")
	if !Encrypted(inside) {
		t.Errorf("Encrypted(%q) = false, want true", inside)
	}
	outside := filepath.Join(t.TempDir(), "metadata.json")
	if Encrypted(outside) {
		t.Errorf("Encrypted(%q) = true, want false", outside)
	}
	// Enabled still requires a protected file name.
	if Enabled(inside) {
		t.Errorf("Enabled(%q) = true, want false", inside)
	}
}

func TestWrongKeyAndPlaintextMigrationBlocked(t *testing.T) {
	root := t.TempDir()
	key := bytes.Repeat([]byte{0x11}, 32)
	release, err := Unlock(root, key)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := RequireKey(root); !errors.Is(err, ErrLocked) {
		t.Fatalf("missing lock check: %v", err)
	}
	if _, err := Unlock(root, bytes.Repeat([]byte{0x22}, 32)); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("wrong key accepted: %v", err)
	}
	release, err = Unlock(root, key)
	if err != nil {
		t.Fatal(err)
	}
	release()
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "legacy.bin"), []byte("plaintext"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Unlock(other, key); !errors.Is(err, ErrPlaintext) {
		t.Fatalf("plaintext migration was allowed: %v", err)
	}
}
