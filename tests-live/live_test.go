// Package live runs each small application under tests-live as a black-box
// check of the metrics package. Every app is a real main program that opens,
// writes, selects, and closes a database in its own temp directory. An app
// exits 0 and prints PASS on success, or exits non-zero on any mismatch.
//
// The apps use only the public metrics API plus the standard library, exactly
// like an external application would.
package live

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// apps lists the live-application directories. Each must contain a main
// package with no test files of its own.
var apps = []string{
	"write-select",
	"batch-query",
	"time-ranges",
	"stats-counters",
	"status-panel",
	"persist-reopen",
	"retention-reject",
	"invalid-input",
	"concurrent-writers",
	"encryption-roundtrip",
	"close-semantics",
	"unicode-labels",
	"empty-db",
	"background-select",
	"read-only",
	"open-options",
	"limits",
	"locked-database",
	"crash-recovery",
	"close-race",
	"multi-day",
	"dedup-truncate",
	"local-time",
	"exact-match",
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(file))
}

func TestLiveApps(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	root := moduleRoot(t)
	for _, app := range apps {
		t.Run(app, func(t *testing.T) {
			t.Parallel()
			appDir := filepath.Join(root, "tests-live", app)
			bin := filepath.Join(t.TempDir(), "app")
			buildCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			build := exec.CommandContext(buildCtx, "go", "build", "-o", bin, ".")
			build.Dir = appDir
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build %s: %v\n%s", app, err, out)
			}
			runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			run := exec.CommandContext(runCtx, bin)
			run.Dir = t.TempDir()
			var stdout, stderr bytes.Buffer
			run.Stdout = &stdout
			run.Stderr = &stderr
			if err := run.Run(); err != nil {
				t.Fatalf("run %s: %v\nstdout:\n%s\nstderr:\n%s", app, err, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), "PASS") {
				t.Fatalf("run %s: missing PASS marker\nstdout:\n%s\nstderr:\n%s", app, stdout.String(), stderr.String())
			}
		})
	}
}
