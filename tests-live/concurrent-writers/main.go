// Command concurrent-writers proves parallel writers land every point
// exactly once.
package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/marcgauthier/metrics"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS: concurrent-writers")
}

func run() error {
	dir, err := os.MkdirTemp("", "metrics-live-concurrent-writers-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := metrics.Open(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	const writers, perWriter = 8, 25
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-2 * time.Second)
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for id := 0; id < writers; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				ts := base.Add(time.Duration(id*perWriter+i) * time.Millisecond)
				if err := db.Write(metrics.Sample{
					Metric: "parallel_marker",
					Labels: map[string]string{"writer": fmt.Sprint(id)},
					Time:   ts,
					Value:  float64(i),
				}); err != nil {
					errs <- err
					return
				}
			}
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		return err
	}
	if err := db.Flush(); err != nil {
		return err
	}
	series, err := db.Select(context.Background(), metrics.Selector{Metric: "parallel_marker"}, metrics.TimeRange{}, writers+1)
	if err != nil {
		return err
	}
	if len(series) != writers {
		return fmt.Errorf("got %d series, want %d", len(series), writers)
	}
	var points int
	for _, s := range series {
		if len(s.Points) != perWriter {
			return fmt.Errorf("writer %v: got %d points, want %d", s.Labels, len(s.Points), perWriter)
		}
		seen := make(map[time.Time]bool, perWriter)
		for _, p := range s.Points {
			if seen[p.Time] {
				return fmt.Errorf("writer %v: duplicate point at %v", s.Labels, p.Time)
			}
			seen[p.Time] = true
		}
		points += len(s.Points)
	}
	if points != writers*perWriter {
		return fmt.Errorf("got %d points, want %d", points, writers*perWriter)
	}
	return nil
}
