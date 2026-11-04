package matching

import (
	"context"
	"errors"
	"time"

	"ridepulse/pkg/events"
	"ridepulse/services/matching-service/internal/metrics"
	"ridepulse/services/matching-service/internal/ports"
)

const searchRadiusKM = 50

// ErrNoDriversAvailable means no free driver is in range. It is a business
// outcome, not an infrastructure failure, so it is not retried.
var ErrNoDriversAvailable = errors.New("no drivers available")

type Service struct {
	repo    ports.DriverRepository
	timeout time.Duration
}

// New builds a matcher. timeout bounds one claim attempt against Redis.
func New(repo ports.DriverRepository, timeout time.Duration) *Service {
	return &Service{repo: repo, timeout: timeout}
}

// Match claims the nearest free driver for the ride. Any other error than
// ErrNoDriversAvailable leaves the outcome unknown: the claim may have been
// applied in Redis with the reply lost. Callers retry those errors, and the
// retry replays the existing assignment instead of assigning a second driver.
func (s *Service) Match(ctx context.Context, event events.RidePricedEvent) (events.RideMatchedEvent, error) {
	start := time.Now()
	metrics.MatchRequests.Inc()
	defer func() {
		metrics.MatchLatency.Observe(time.Since(start).Seconds())
	}()

	// Detached from shutdown cancellation, so a claim in flight gets its reply.
	claimCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.timeout)
	defer cancel()

	claim, err := s.repo.ClaimNearestDriver(claimCtx, event.RideID, event.Pickup.Lat, event.Pickup.Lng, searchRadiusKM)
	if err != nil {
		metrics.MatchFailures.Inc()
		return events.RideMatchedEvent{}, err
	}
	if claim.DriverID == "" {
		metrics.MatchFailures.Inc()
		return events.RideMatchedEvent{}, ErrNoDriversAvailable
	}
	if claim.Replayed {
		metrics.MatchIdempotentReplays.Inc()
	} else {
		metrics.MatchSuccess.Inc()
	}
	return events.RideMatchedEvent{
		Version:         events.Version,
		RideID:          event.RideID,
		DriverID:        claim.DriverID,
		MatchedAt:       claim.ClaimedAt,
		RequestedAt:     event.RequestedAt,
		Price:           event.Price,
		Currency:        event.Currency,
		SurgeMultiplier: event.SurgeMultiplier,
	}, nil
}
