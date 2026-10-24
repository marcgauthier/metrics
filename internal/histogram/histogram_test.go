package histogram

import (
	"math"
	"testing"
)

func TestFastQuantilesBasic(t *testing.T) {
	f := NewFast()
	for i := 1; i <= 100; i++ {
		f.Update(float64(i))
	}
	if got := f.Quantile(0); got != 1 {
		t.Fatalf("min: got %v, want 1", got)
	}
	if got := f.Quantile(1); got != 100 {
		t.Fatalf("max: got %v, want 100", got)
	}
	median := f.Quantile(0.5)
	if median < 45 || median > 55 {
		t.Fatalf("median: got %v, want ~50", median)
	}
	if got := NewFast().Quantile(0.5); !math.IsNaN(got) {
		t.Fatalf("empty: got %v, want NaN", got)
	}
}

func TestFastResetIsRepeatable(t *testing.T) {
	feed := func(f *Fast) []float64 {
		for i := 0; i < 5000; i++ {
			f.Update(float64(i % 997))
		}
		return f.Quantiles(nil, []float64{0.5, 0.9, 0.99})
	}
	f := NewFast()
	first := feed(f)
	f.Reset()
	second := feed(f)
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("run differs after Reset: %v vs %v", first, second)
		}
	}
}

func TestUint32nRange(t *testing.T) {
	if got := uint32nRange(0xFFFFFFFF, 0); got != 0 {
		t.Fatalf("maxN=0: got %d, want 0", got)
	}
	// Multiply-shift reduction must match fastrand.Uint32n exactly.
	for _, maxN := range []uint32{1, 2, 1000, 1 << 31, 0xFFFFFFFF} {
		for _, x := range []uint32{0, 1, 0x80000000, 0xFFFFFFFF} {
			want := uint32((uint64(x) * uint64(maxN)) >> 32)
			if got := uint32nRange(x, maxN); got != want {
				t.Fatalf("uint32nRange(%d, %d): got %d, want %d", x, maxN, got, want)
			}
		}
	}
}

func TestFastCountWraparound(t *testing.T) {
	f := NewFast()
	for i := 0; i < maxSamples; i++ {
		f.Update(float64(i))
	}
	// uint32(count) wraps to 0 at 2^32 updates; sampling must not panic.
	f.count = 1<<32 - 1
	f.Update(1e9)
	f.count = 1 << 32
	f.Update(1e9)
	f.count = 1<<33 + 7
	f.Update(1e9)
}

func TestFastZeroValueUsable(t *testing.T) {
	var f Fast
	f.Update(42)
	if got := f.Quantile(1); got != 42 {
		t.Fatalf("got %v, want 42", got)
	}
}
