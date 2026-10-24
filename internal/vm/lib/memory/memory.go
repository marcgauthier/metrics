package memory

import (
	"math"
	"sync"

	"github.com/marcgauthier/metrics/internal/vm-metrics"
	"github.com/marcgauthier/metrics/internal/vm/lib/logger"
)

// This is an embedded library and registers no command-line flags. These are
// plain settings with the upstream defaults: caches may occupy 60% of system
// memory. allowedBytes overrides allowedPercent when set to a non-zero value.
var (
	allowedPercent = 60.0
	allowedBytes   = int64(0)
)

var _ = metrics.NewGauge("process_memory_limit_bytes", func() float64 {
	return float64(memoryLimit)
})

var (
	allowedMemory   int
	remainingMemory int
	memoryLimit     int
)
var once sync.Once

func initOnce() {
	memoryLimit = sysTotalMemory()
	if allowedBytes <= 0 {
		if allowedPercent < 1 || allowedPercent > 100 {
			logger.Fatalf("FATAL: memory.allowedPercent must be in the range [1...100]; got %g", allowedPercent)
		}
		percent := allowedPercent / 100
		allowedMemory = int(float64(memoryLimit) * percent)
		remainingMemory = memoryLimit - allowedMemory
		if remainingMemory <= 0 {
			logger.Fatalf("BUG: remaining memory %d bytes cannot be less than or equal to zero, detected system memory limit %d bytes, memory.allowedPercent=%g", remainingMemory, memoryLimit, allowedPercent)
		}
		logger.Infof("limiting caches to %d bytes, leaving %d bytes to the OS according to memory.allowedPercent=%g, system memory limit %d bytes", allowedMemory, remainingMemory, allowedPercent, memoryLimit)
	} else {
		allowedMemory = intCapped(allowedBytes)
		if allowedMemory < 1*1024*1024 {
			// It's fair to print a hint if the allowedBytes is set to too small, typically by misconfiguration.
			logger.Warnf("allowed memory %d bytes set by memory.allowedBytes is low. The process may behave unexpectedly.", allowedMemory)
		}
		remainingMemory = memoryLimit - allowedMemory
		if remainingMemory <= 0 {
			logger.Fatalf("FATAL: remaining memory %d bytes cannot be less than or equal to zero, detected system memory limit %d bytes, memory.allowedBytes=%d", remainingMemory, memoryLimit, allowedBytes)
		}
		logger.Infof("limiting caches to %d bytes, leaving %d bytes to the OS according to memory.allowedBytes=%d, system memory limit %d bytes", allowedMemory, remainingMemory, allowedBytes, memoryLimit)
	}

}

// intCapped converts n to int, capping values outside the int range.
func intCapped(n int64) int {
	if n > int64(math.MaxInt) {
		return math.MaxInt
	}
	if n < int64(math.MinInt) {
		return math.MinInt
	}
	return int(n)
}

// Allowed returns the amount of system memory allowed to use by the app.
func Allowed() int {
	once.Do(initOnce)
	return allowedMemory
}

// Remaining returns the amount of memory remaining to the OS.
func Remaining() int {
	once.Do(initOnce)
	return remainingMemory
}
