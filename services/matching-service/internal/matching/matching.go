package matching

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"ridepulse/services/matching-service/internal/domain"
	"ridepulse/services/matching-service/internal/metrics"
	"ridepulse/services/matching-service/internal/ports"
)

type Service struct {
	repo ports.DriverRepository
}

func New(repo ports.DriverRepository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Match(ctx context.Context, event domain.RidePricedEvent) (domain.RideMatchedEvent, error) {

	start := time.Now()
	metrics.MatchRequests.Inc()

	// Record latency once at exit
	defer func() {
		metrics.MatchLatency.Observe(time.Since(start).Seconds())
	}()

	drivers, err := s.repo.FindNearbyDrivers(ctx, event.Pickup.Lat, event.Pickup.Lng, 50)
	if err != nil {
		metrics.MatchFailures.Inc()
		return domain.RideMatchedEvent{}, err
	}

	metrics.NearbyDrivers.Observe(float64(len(drivers)))

	if len(drivers) == 0 {
		metrics.MatchFailures.Inc()
		return domain.RideMatchedEvent{}, errors.New("no drivers available")
	}

	// Shuffle for fairness
	rand.Shuffle(len(drivers), func(i, j int) {
		drivers[i], drivers[j] = drivers[j], drivers[i]
	})

	const batchSize = 5

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

		batchCtx, cancel := context.WithCancel(ctx)
		resultCh := make(chan string, 1)

		for _, d := range batch {
			driverID := d

			go func() {
				ok, err := s.repo.TryLockDriver(batchCtx, driverID, event.RideID)
				if err != nil {
					return
				}

				if ok {
					select {
					case resultCh <- driverID:
						cancel()
					default:
					}
				}
			}()
		}

		select {
		case driverID := <-resultCh:
			cancel()
			metrics.MatchSuccess.Inc()

			return domain.RideMatchedEvent{
				RideID:    event.RideID,
				DriverID:  driverID,
				MatchedAT: time.Now().Unix(),
			}, nil

		case <-time.After(400 * time.Millisecond):
			cancel()
		}
	}

	metrics.MatchFailures.Inc()
	return domain.RideMatchedEvent{}, errors.New("no drivers available")
}
