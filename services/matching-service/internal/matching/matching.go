package matching

import (
	"context"
	"errors"
	"log"
	"math/rand"
	"time"

	"ridepulse/services/matching-service/internal/domain"
	"ridepulse/services/matching-service/internal/ports"
)

type Service struct {
	repo ports.DriverRepository
}

func New(repo ports.DriverRepository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Match(ctx context.Context, event domain.RidePricedEvent) (domain.RideMatchedEvent, error) {

	log.Printf(
		"Matching ride %s at pickup: lat=%f lng=%f",
		event.RideID,
		event.Pickup.Lat,
		event.Pickup.Lng,
	)

	drivers, err := s.repo.FindNearbyDrivers(ctx, event.Pickup.Lat, event.Pickup.Lng, 50) //query all drivers within 50km
	if err != nil {
		return domain.RideMatchedEvent{}, err
	}

	log.Printf("Found %d nearby drivers", len(drivers))

	if len(drivers) == 0 {
		//no driver found
		return domain.RideMatchedEvent{}, errors.New("no drivers available")
	}

	//shuffling drivers so that all driver have equal chances to get matched .
	// --- Shuffle drivers ---
	rand.Shuffle(len(drivers), func(i, j int) {
		drivers[i], drivers[j] = drivers[j], drivers[i]
	})

	const batchSize = 5

	for i := 0; i < len(drivers); i += batchSize {

		end := i + batchSize
		if end > len(drivers) {
			end = len(drivers)
		}

		batch := drivers[i:end]

		ctx, cancel := context.WithCancel(context.Background())
		resultCh := make(chan string, 1)

		// Parallel lock attempts for batch
		for _, d := range batch {
			driverID := d

			go func() {
				select {
				case <-ctx.Done():
					return
				default:
				}

				log.Printf("Trying driver: %s", driverID)

				ok, err := s.repo.TryLockDriver(ctx, driverID, event.RideID)
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

		// Wait for success in this batch
		select {
		case driverID := <-resultCh:
			cancel()
			log.Printf("Locked driver: %s", driverID)

			return domain.RideMatchedEvent{
				RideID:    event.RideID,
				DriverID:  driverID,
				MatchedAT: time.Now().Unix(),
			}, nil

		case <-time.After(400 * time.Millisecond):
			cancel()
		}
	}

	return domain.RideMatchedEvent{}, errors.New("no drivers available")

}
