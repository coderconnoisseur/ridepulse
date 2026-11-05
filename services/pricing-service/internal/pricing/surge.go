package pricing

import (
	"context"
	"log"
	"math"
	"sync/atomic"
	"time"
)

const (
	// surgeWindow is how many ticks of demand count as "recent".
	surgeWindow = 5
	// SurgeTickInterval is how often the multiplier is recalculated.
	SurgeTickInterval = time.Second

	maxSurge = 3.0
)

// SurgeMultiplier compares rides requested in the recent window with drivers
// free right now. It is 1.0 until demand exceeds supply, then rises with the
// ratio up to maxSurge, in steps of 0.1. It applies city-wide: there are no
// surge zones.
func SurgeMultiplier(recentRides, freeDrivers int64) float64 {
	if recentRides == 0 {
		return 1.0
	}
	if freeDrivers == 0 {
		return maxSurge
	}
	ratio := float64(recentRides) / float64(freeDrivers)
	return math.Round(max(1.0, min(ratio, maxSurge))*10) / 10
}

// SurgeTracker holds the current multiplier so pricing a ride never waits on
// Redis. RecordRide and Current are safe for concurrent use; Tick must be
// called from one goroutine.
type SurgeTracker struct {
	sinceTick  atomic.Int64
	multiplier atomic.Uint64 // math.Float64bits of the current multiplier

	window [surgeWindow]int64
	next   int
}

// NewSurgeTracker returns a tracker with no demand and a multiplier of 1.0.
func NewSurgeTracker() *SurgeTracker {
	s := &SurgeTracker{}
	s.set(1.0)
	return s
}

// RecordRide counts one ride request towards recent demand.
func (s *SurgeTracker) RecordRide() { s.sinceTick.Add(1) }

// Current returns the multiplier to charge now.
func (s *SurgeTracker) Current() float64 { return math.Float64frombits(s.multiplier.Load()) }

// Tick closes the current one-tick bucket and recalculates the multiplier.
func (s *SurgeTracker) Tick(freeDrivers int64) {
	s.rotate()
	var recent int64
	for _, n := range s.window {
		recent += n
	}
	s.set(SurgeMultiplier(recent, freeDrivers))
}

func (s *SurgeTracker) rotate() {
	s.window[s.next] = s.sinceTick.Swap(0)
	s.next = (s.next + 1) % surgeWindow
}

func (s *SurgeTracker) set(v float64) { s.multiplier.Store(math.Float64bits(v)) }

// RunSurge updates s every interval until ctx is cancelled. If the driver count
// is unavailable, riders are charged the normal fare rather than a guess.
func RunSurge(ctx context.Context, s *SurgeTracker, freeDrivers func(context.Context) (int64, error), interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		countCtx, cancel := context.WithTimeout(ctx, interval)
		free, err := freeDrivers(countCtx)
		cancel()
		if err != nil {
			if !failing && ctx.Err() == nil {
				log.Printf("surge: driver count unavailable, charging normal fares: %v", err)
			}
			failing = true
			s.rotate()
			s.set(1.0)
			continue
		}
		if failing {
			log.Printf("surge: driver count available again")
		}
		failing = false
		s.Tick(free)
	}
}
