// Command crash-recovery proves the durability contract after an
// unclean shutdown: a writer child exits via os.Exit without Close (no
// deferred cleanup runs, exactly like a crash from the database's point of
// view). Unflushed samples are lost but the database reopens cleanly;
// samples that reached disk through the background flush survive.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/marcgauthier/metrics"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "child" {
		childMain(os.Args[2], os.Args[3] == "settled")
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS: crash-recovery")
}

func childMain(dir string, settled bool) {
	db, err := metrics.Open(dir, metrics.WithFlushInterval(time.Second))
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	marker := "crash_volatile"
	if settled {
		marker = "crash_settled"
	}
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	for i := 0; i < 50; i++ {
		if err := db.Write(metrics.Sample{
			Metric: marker,
			Time:   base.Add(time.Duration(i) * time.Second),
			Value:  float64(i),
		}); err != nil {
			fmt.Fprintln(os.Stderr, "FAIL:", err)
			os.Exit(1)
		}
	}
	if settled {
		if err := db.Flush(); err != nil {
			fmt.Fprintln(os.Stderr, "FAIL:", err)
			os.Exit(1)
		}
		time.Sleep(8 * time.Second)
	}
	os.Exit(3)
}

func run() error {
	if err := scenario("settled", true, 50); err != nil {
		return err
	}
	return scenario("volatile", false, 0)
}

func scenario(name string, settled bool, wantPoints int) error {
	dir, err := os.MkdirTemp("", "metrics-live-crash-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	self, err := os.Executable()
	if err != nil {
		return err
	}
	mode := "volatile"
	if settled {
		mode = "settled"
	}
	cmd := exec.Command(self, "child", dir, mode)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 3 {
		return fmt.Errorf("%s: child exit=%v, want exit code 3\nstdout:\n%s\nstderr:\n%s",
			name, err, stdout.String(), stderr.String())
	}
	db, err := metrics.Open(dir)
	if err != nil {
		return fmt.Errorf("%s: reopen: %w", name, err)
	}
	defer db.Close()
	series, err := db.Select(context.Background(), metrics.Selector{Metric: "crash_" + mode}, metrics.TimeRange{}, 10)
	if err != nil {
		return fmt.Errorf("%s: select: %w", name, err)
	}
	got := 0
	if len(series) == 1 {
		got = len(series[0].Points)
	} else if len(series) != 0 {
		return fmt.Errorf("%s: got %d series, want at most 1", name, len(series))
	}
	if got != wantPoints {
		return fmt.Errorf("%s: got %d points, want %d", name, got, wantPoints)
	}
	return nil
}
