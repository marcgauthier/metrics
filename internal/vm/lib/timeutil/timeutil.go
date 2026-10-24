package timeutil

import (
	"math/rand/v2"
	"time"
)

// AddJitterToDuration adds up to 10% random jitter to d and returns the resulting duration.
//
// The maximum jitter is limited by 10 seconds.
func AddJitterToDuration(d time.Duration) time.Duration {
	dv := min(d/10, 10*time.Second)
	// math/rand/v2 replaces the upstream fastrand dependency.
	p := rand.Float64()
	return d + time.Duration(p*float64(dv))
}
