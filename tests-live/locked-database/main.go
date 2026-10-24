// Command locked-database proves opening an already-open database fails
// with an error instead of crashing: same-process double open,
// cross-process contention, and a directory without write permission. The
// holder database keeps working after the failed attempts.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/marcgauthier/metrics"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "child" {
		if err := childMain(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "FAIL:", err)
			os.Exit(1)
		}
		fmt.Println("PASS: locked-database-child")
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS: locked-database")
}

// childMain runs in a separate process while the parent holds the database.
func childMain(dir string) error {
	db, err := metrics.Open(dir)
	if err == nil {
		db.Close()
		return fmt.Errorf("second process unexpectedly opened the database")
	}
	return nil
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-locked-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	holder, err := metrics.Open(dir)
	if err != nil {
		return err
	}
	defer holder.Close()
	if db, err := metrics.Open(dir); err == nil {
		db.Close()
		return fmt.Errorf("same-process double open: unexpectedly succeeded")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "child", dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("child opener: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), tail(stderr.String()))
	}
	if !strings.Contains(stdout.String(), "PASS") {
		return fmt.Errorf("child opener: missing PASS\nstdout:\n%s\nstderr:\n%s", stdout.String(), tail(stderr.String()))
	}
	if err := checkNoWritePermission(); err != nil {
		return err
	}
	if err := holder.Write(metrics.Sample{Metric: "holder_healthy_marker", Value: 1}); err != nil {
		return fmt.Errorf("holder write after contention: %w", err)
	}
	if err := holder.Flush(); err != nil {
		return err
	}
	series, err := holder.Select(context.Background(), metrics.Selector{Metric: "holder_healthy_marker"}, metrics.TimeRange{}, 10)
	if err != nil {
		return err
	}
	if len(series) != 1 {
		return fmt.Errorf("holder select: got %d series, want 1", len(series))
	}
	return nil
}

func checkNoWritePermission() error {
	root, err := os.MkdirTemp("", "metrics-live-noperm-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0755); err != nil {
		return err
	}
	if err := os.Chmod(locked, 0500); err != nil {
		return err
	}
	defer os.Chmod(locked, 0700)
	if db, err := metrics.Open(locked); err == nil {
		db.Close()
		return fmt.Errorf("unwritable dir: unexpectedly opened")
	}
	return nil
}

func tail(s string) string {
	if len(s) > 2000 {
		return s[len(s)-2000:]
	}
	return s
}
