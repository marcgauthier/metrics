package metrics

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestMain re-executes the test binary as a child process that exercises the
// full Open/Write/Select/Close path without ever calling flag.Parse, proving
// metrics works as an embedded library in hosts that do not parse flags.
//
// When TestMain is called, flag.Parse has not run yet (parsing happens inside
// m.Run), so a child that returns from TestMain early genuinely tests the
// unparsed-flags path. The child asserts flag.Parsed() is false to guard the
// premise.
func TestMain(m *testing.M) {
	if os.Getenv("METRICS_TEST_NOFLAG_CHILD") == "1" {
		os.Exit(runNoFlagParseChild())
	}
	os.Exit(m.Run())
}

func TestOpenWriteSelectWithoutFlagParse(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		"METRICS_TEST_NOFLAG_CHILD=1",
		"METRICS_TEST_NOFLAG_DIR="+t.TempDir(),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("metrics requires flag.Parse: child failed: %v\n%s", err, out)
	}
}

func runNoFlagParseChild() int {
	if flag.Parsed() {
		fmt.Fprintln(os.Stderr, "test premise broken: flags were parsed in child")
		return 1
	}
	fail := func(format string, args ...any) int {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		return 1
	}
	// Importing metrics must not register command-line flags on the host's
	// flag set. Only the testing framework's own test.* flags may exist.
	var registered []string
	flag.CommandLine.VisitAll(func(f *flag.Flag) {
		if !strings.HasPrefix(f.Name, "test.") {
			registered = append(registered, f.Name)
		}
	})
	if len(registered) > 0 {
		return fail("metrics registered command-line flags: %v", registered)
	}
	db, err := Open(os.Getenv("METRICS_TEST_NOFLAG_DIR"))
	if err != nil {
		return fail("Open: %v", err)
	}
	defer db.Close()
	if err := db.Write(Sample{Metric: "noflag_marker", Value: 1}); err != nil {
		return fail("Write: %v", err)
	}
	if err := db.Flush(); err != nil {
		return fail("Flush: %v", err)
	}
	series, err := db.Select(context.Background(), Selector{Metric: "noflag_marker"}, TimeRange{}, 10)
	if err != nil {
		return fail("Select: %v", err)
	}
	if len(series) != 1 || series[0].Metric != "noflag_marker" || len(series[0].Points) != 1 {
		return fail("unexpected series: %#v", series)
	}
	return 0
}
