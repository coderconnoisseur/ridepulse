package ports

import "context"

type DriverRepository interface {
	// ClaimNearestDriver atomically assigns the nearest free driver within
	// radiusKM to rideID. If rideID already has a driver, that driver is
	// returned with replayed = true and nothing changes. An empty driverID
	// means no free driver is in range.
	ClaimNearestDriver(ctx context.Context, rideID string, lat, lng, radiusKM float64) (driverID string, replayed bool, err error)
}
