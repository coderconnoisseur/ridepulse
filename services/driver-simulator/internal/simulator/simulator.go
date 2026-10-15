package simulator

import (
	"context"
	"fmt"
	"log"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"ridepulse/services/driver-simulator/internal/redis"
)

type Driver struct {
	ID  string
	Lat float64
	Lng float64
}

type Simulator struct {
	drivers      []Driver
	tripDuration time.Duration
}

var (
	updates        int64
	tripsCompleted int64
)

func New(numDrivers int, tripDuration time.Duration) *Simulator {
	drivers := make([]Driver, numDrivers)

	for i := 0; i < numDrivers; i++ {
		drivers[i] = Driver{
			ID:  fmt.Sprintf("driver-%d", i),
			Lat: 12.9 + rand.Float64()*0.02,
			Lng: 77.6 + rand.Float64()*0.02,
		}
	}
	return &Simulator{drivers: drivers, tripDuration: tripDuration}
}

func (d *Driver) Move() {
	d.Lat += (rand.Float64() - 0.5) * 0.001
	d.Lng += (rand.Float64() - 0.5) * 0.001
}

// Run starts the stats logger and one goroutine per driver, and blocks until
// ctx is cancelled and all of them have exited.
func (s *Simulator) Run(ctx context.Context, redisClient *redis.Client) {
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				count := atomic.SwapInt64(&updates, 0)
				trips := atomic.SwapInt64(&tripsCompleted, 0)
				log.Printf("Drivers updates /sec : %d, trips completed /sec : %d", count, trips)
			case <-ctx.Done():
				return
			}
		}
	}()

	for i := range s.drivers {
		wg.Add(1)
		go func(d *Driver) {
			defer wg.Done()
			s.runDriver(ctx, d, redisClient)
		}(&s.drivers[i])
	}

	wg.Wait()
}

func (s *Simulator) runDriver(ctx context.Context, d *Driver, redisClient *redis.Client) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	// Trip state is local to this driver's goroutine.
	var (
		rideID        string
		tripStart     time.Time
		releaseFailed bool
	)

	for {
		select {
		case <-ticker.C:
			d.Move()
			assigned, err := redisClient.UpdateDriverLocation(ctx, d.ID, d.Lat, d.Lng)
			if err != nil {
				continue
			}
			atomic.AddInt64(&updates, 1)

			if assigned == "" {
				// Not assigned (or the assignment was cleared elsewhere).
				rideID = ""
				continue
			}
			if assigned != rideID {
				rideID, tripStart, releaseFailed = assigned, time.Now(), false
			}
			if time.Since(tripStart) < s.tripDuration {
				continue
			}
			if err := redisClient.ReleaseDriver(ctx, d.ID, rideID); err != nil {
				// Keep the trip state and retry on the next tick.
				if !releaseFailed && ctx.Err() == nil {
					log.Printf("release %s from ride %s failed: %v", d.ID, rideID, err)
				}
				releaseFailed = true
				continue
			}
			rideID, releaseFailed = "", false
			atomic.AddInt64(&tripsCompleted, 1)
		case <-ctx.Done():
			return
		}
	}
}
