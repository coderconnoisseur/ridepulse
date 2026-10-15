package matching

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"

	"ridepulse/services/matching-service/internal/domain"
	"ridepulse/services/matching-service/internal/metrics"
	"ridepulse/services/matching-service/internal/ports"
)

// BatchDeadline is the wall-clock budget for a single batch of parallel
// SETNX attempts. It is exported so callers can size their outer match
// deadline as (BatchDeadline * desiredBatchAttempts) — the outer deadline
// MUST be greater than BatchDeadline or progressive batching collapses to
// a single batch attempt per ride (which is exactly the bug this constant
// was reduced from 400 ms to 200 ms to prevent under the earlier 300 ms
// outer deadline).
const BatchDeadline = 200 * time.Millisecond

const (
	batchSize         = 5
	batchDeadline     = BatchDeadline
	releaseTimeout    = 200 * time.Millisecond
	assignTimeout     = 200 * time.Millisecond
	reaperGracePeriod = 500 * time.Millisecond

	// driverTierSize groups nearby drivers into "distance tiers" that are
	// shuffled internally to spread lock contention across drivers of
	// similar proximity, without ever trying a farther driver before a
	// nearer one. See shuffleWithinTiers for the full rationale.
	driverTierSize = 10
)

// ErrNoDriversAvailable means no free driver could be locked for the ride. It
// is a business outcome, not an infrastructure failure, so it is not retried.
var ErrNoDriversAvailable = errors.New("no drivers available")

type Service struct {
	repo ports.DriverRepository
}

func New(repo ports.DriverRepository) *Service {
	return &Service{repo: repo}
}

// shuffleWithinTiers randomizes the order of `drivers` within contiguous
// windows of `tierSize`, leaving the coarse ordering across tiers intact.
//
// Given a nearest-first input like [d0, d1, d2, …, d19] and tierSize=10,
// tier 1 covers positions 0..9 (the 10 nearest) and tier 2 covers 10..19.
// After the call, tier 1's drivers are still all closer than tier 2's, but
// which of tier 1's drivers land in the first parallel batch is now random
// across concurrent rides. Under a hotspot (all rides at the same pickup)
// this halves contention on the top-5 nearest — each of the top-10 has a
// 5/10 chance of being in any given ride's first batch, instead of the
// same 5 being tried by every ride deterministically.
//
// Under NON-hotspot load (rides spread across the map), each ride's
// nearby set is different, so this function is a near-no-op and pure
// nearest-first behaviour is recovered.
func shuffleWithinTiers(drivers []string, tierSize int) {
	if tierSize < 2 {
		return
	}
	for i := 0; i < len(drivers); i += tierSize {
		end := i + tierSize
		if end > len(drivers) {
			end = len(drivers)
		}
		tier := drivers[i:end]
		rand.Shuffle(len(tier), func(x, y int) {
			tier[x], tier[y] = tier[y], tier[x]
		})
	}
}

func (s *Service) Match(ctx context.Context, event domain.RidePricedEvent) (domain.RideMatchedEvent, error) {

	start := time.Now()
	metrics.MatchRequests.Inc()

	defer func() {
		metrics.MatchLatency.Observe(time.Since(start).Seconds())
	}()

	// Idempotent replay: a retry or redelivery of a ride that already has a
	// driver must return that driver, never lock and assign a second one.
	if driverID, found, err := s.repo.GetRideDriver(ctx, event.RideID); err != nil {
		metrics.MatchFailures.Inc()
		return domain.RideMatchedEvent{}, err
	} else if found {
		metrics.MatchIdempotentReplays.Inc()
		return matchedEvent(event, driverID), nil
	}

	drivers, err := s.repo.FindNearbyDrivers(ctx, event.Pickup.Lat, event.Pickup.Lng, 50)
	if err != nil {
		metrics.MatchFailures.Inc()
		return domain.RideMatchedEvent{}, err
	}

	metrics.NearbyDrivers.Observe(float64(len(drivers)))

	if len(drivers) == 0 {
		metrics.MatchFailures.Inc()
		return domain.RideMatchedEvent{}, ErrNoDriversAvailable
	}

	// Shuffle within distance tiers, NOT across the whole list.
	//
	// Redis GEORADIUS returns drivers sorted nearest-first, which is the
	// signal we want to preserve: assigning the nearest available driver
	// is the whole point of geospatial matching. A full-list rand.Shuffle
	// (the previous behaviour) destroys that signal and can hand a rider
	// a 5 km driver when a 500 m one was available.
	//
	// But a strict nearest-first policy also fails under hotspot load: N
	// concurrent rides at the same pickup all try the exact same top-5
	// drivers, guarantee lock conflicts, and cascade batch failures. So
	// we compromise: preserve the coarse nearest-first ordering across
	// tiers, but randomize which drivers land in each parallel batch
	// within a tier. Contention is spread over ~driverTierSize drivers
	// per tier instead of being pinned to a fixed 5.
	shuffleWithinTiers(drivers, driverTierSize)

	for i := 0; i < len(drivers); i += batchSize {

		select {
		case <-ctx.Done():
			metrics.MatchFailures.Inc()
			return domain.RideMatchedEvent{}, ctx.Err()
		default:
		}

		end := i + batchSize
		if end > len(drivers) {
			end = len(drivers)
		}

		batch := drivers[i:end]

		winnerID, ok := s.tryLockBatch(ctx, batch, event.RideID)
		if !ok {
			continue
		}

		// The returned driver may differ from winnerID if a concurrent
		// attempt for the same ride assigned first; our lock is then freed.
		assignedID, err := s.assign(ctx, winnerID, event.RideID)
		if errors.Is(err, ports.ErrLockLost) {
			// Lock expired between SETNX and assign; it is no longer ours
			// to release. Try the next batch.
			continue
		}
		if err != nil {
			s.releaseLock(winnerID, event.RideID)
			metrics.MatchFailures.Inc()
			return domain.RideMatchedEvent{}, err
		}
		metrics.MatchSuccess.Inc()
		return matchedEvent(event, assignedID), nil
	}

	metrics.MatchFailures.Inc()
	return domain.RideMatchedEvent{}, ErrNoDriversAvailable
}

// assign runs AssignDriver on a context detached from the match deadline
// (same rationale as releaseLock): once we hold a lock, the outer deadline
// must not cut the assign reply mid-flight and leave a ride assigned in
// Redis that the caller believes failed. Values (trace IDs etc.) are kept.
func (s *Service) assign(ctx context.Context, driverID, rideID string) (string, error) {
	assignCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), assignTimeout)
	defer cancel()
	return s.repo.AssignDriver(assignCtx, driverID, rideID)
}

func matchedEvent(event domain.RidePricedEvent, driverID string) domain.RideMatchedEvent {
	return domain.RideMatchedEvent{
		RideID:      event.RideID,
		DriverID:    driverID,
		MatchedAT:   time.Now().Unix(),
		RequestedAt: event.RequestedAt,
	}
}

// tryLockBatch races SETNX across the whole batch, picks the first successful
// lock as the batch winner, and releases every OTHER successful lock so it
// does not sit in Redis unused for the lock TTL.
//
// The previous implementation used a capacity-1 result channel: only the
// first-arriving successful lock was captured, every additional successful
// lock landed in the select's default branch and was silently dropped —
// leaving a "phantom lock" on that driver for the 3-second TTL. Under
// contention that was the dominant driver-availability leak.
//
// This implementation buffers the result channel to len(batch), collects
// the first arrival as the winner, and either releases subsequent arrivals
// inline (before we return) or hands them to a reaper goroutine (if we've
// already returned to the caller). Every SETNX success has exactly one
// terminal fate: it becomes the winner or it is explicitly unlocked.
func (s *Service) tryLockBatch(parentCtx context.Context, batch []string, rideID string) (string, bool) {
	batchCtx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	resultCh := make(chan string, len(batch))
	var wg sync.WaitGroup

	for _, driverID := range batch {
		driverID := driverID
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.repo.TryLockDriver(batchCtx, driverID, rideID)
			if err != nil || !ok {
				return
			}
			// Buffered channel sized to len(batch): send never blocks and never
			// falls into a default branch, so no successful SETNX is ever lost.
			resultCh <- driverID
		}()
	}

	// Close resultCh once all attempts have completed so downstream consumers
	// can detect drain-complete with the "channel closed" signal.
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	timer := time.NewTimer(batchDeadline)
	defer timer.Stop()

	for {
		select {
		case driverID, more := <-resultCh:
			if !more {
				// All goroutines finished without producing a winner.
				return "", false
			}
			// First successful lock in this batch → winner.
			// Any subsequent successful locks (still in-flight or already
			// buffered) are phantoms — hand them to the reaper for release.
			cancel()
			go s.reapBatch(resultCh, rideID)
			return driverID, true

		case <-timer.C:
			// Batch deadline expired without a winner. In-flight goroutines
			// may still succeed at SETNX — the reaper will release those.
			cancel()
			go s.reapBatch(resultCh, rideID)
			return "", false

		case <-parentCtx.Done():
			// Outer job deadline hit. Same treatment as timeout.
			cancel()
			go s.reapBatch(resultCh, rideID)
			return "", false
		}
	}
}

// releaseLock unlocks a driver whose lock is no longer wanted, using a fresh
// context so the caller's cancellation cannot cause the release itself to be
// skipped. If the release succeeds, increments the phantom-release counter
// so the effect of this fix is observable in Prometheus.
func (s *Service) releaseLock(driverID, rideID string) {
	ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	if err := s.repo.UnlockDriver(ctx, driverID, rideID); err == nil {
		metrics.DriverLockReleasedPhantom.Inc()
	}
}

// reapBatch drains any late-arriving successful locks from a batch we have
// already returned from and releases them. Runs off the hot path.
//
// Bounded by reaperGracePeriod so a wedged Redis call cannot leak a goroutine
// forever. In the worst case (grace period elapses while a lock is still
// in flight), the lock is not explicitly released here — but the SETNX TTL
// (3s) is still an upper bound on how long that driver stays blocked. That
// is strictly better than the pre-fix behaviour, which held every phantom
// lock for the full TTL as a matter of course.
func (s *Service) reapBatch(resultCh <-chan string, rideID string) {
	grace := time.NewTimer(reaperGracePeriod)
	defer grace.Stop()
	for {
		select {
		case driverID, more := <-resultCh:
			if !more {
				return
			}
			s.releaseLock(driverID, rideID)
		case <-grace.C:
			return
		}
	}
}
