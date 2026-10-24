// Command close-race proves shutdown races are safe: concurrent Close calls
// are all harmless, a Close racing an active streaming query completes
// without deadlock, and writes racing Close either land or report ErrClosed.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/marcgauthier/metrics"
)

func main() {
	time.AfterFunc(60*time.Second, func() {
		fmt.Fprintln(os.Stderr, "FAIL: timed out, likely deadlock")
		os.Exit(1)
	})
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS: close-race")
}

func run() error {
	if err := concurrentClose(); err != nil {
		return err
	}
	if err := closeDuringQuery(); err != nil {
		return err
	}
	return writesRacingClose()
}

func concurrentClose() error {
	dir, err := os.MkdirTemp("", "metrics-live-close-race-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := metrics.Open(dir)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- db.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return fmt.Errorf("concurrent close: %w", err)
		}
	}
	return nil
}

func closeDuringQuery() error {
	dir, err := os.MkdirTemp("", "metrics-live-close-query-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := metrics.Open(dir)
	if err != nil {
		return err
	}
	base := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	samples := make([]metrics.Sample, 0, 2000)
	for i := 0; i < 200; i++ {
		for j := 0; j < 10; j++ {
			samples = append(samples, metrics.Sample{
				Metric: "close_race_series",
				Labels: map[string]string{"shard": fmt.Sprintf("%03d", i)},
				Time:   base.Add(time.Duration(j) * time.Second),
				Value:  float64(j),
			})
		}
	}
	if err := db.WriteBatch(samples); err != nil {
		db.Close()
		return err
	}
	if err := db.Flush(); err != nil {
		db.Close()
		return err
	}
	queryErr := make(chan error, 1)
	go func() {
		queryErr <- db.Query(context.Background(), metrics.Selector{Metric: "close_race_series"}, metrics.TimeRange{},
			func(metrics.Series) error {
				time.Sleep(time.Millisecond)
				return nil
			})
	}()
	time.Sleep(100 * time.Millisecond)
	if err := db.Close(); err != nil {
		return fmt.Errorf("close during query: %w", err)
	}
	if err := <-queryErr; err != nil && !errors.Is(err, metrics.ErrClosed) {
		return fmt.Errorf("query during close: %w", err)
	}
	return nil
}

func writesRacingClose() error {
	dir, err := os.MkdirTemp("", "metrics-live-write-close-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := metrics.Open(dir)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	writeErrs := make(chan error, 200)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if err := db.Write(metrics.Sample{
					Metric: "race_marker",
					Labels: map[string]string{"w": fmt.Sprint(w)},
					Value:  float64(i),
				}); err != nil {
					writeErrs <- err
				}
			}
		}(w)
	}
	go func() {
		time.Sleep(10 * time.Millisecond)
		db.Close()
	}()
	wg.Wait()
	close(writeErrs)
	for err := range writeErrs {
		if !errors.Is(err, metrics.ErrClosed) {
			return fmt.Errorf("write racing close: got %v, want nil or ErrClosed", err)
		}
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("final close: %w", err)
	}
	return nil
}
