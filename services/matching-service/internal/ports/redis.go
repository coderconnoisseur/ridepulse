package ports

import "context"

type DriverRepository interface {
	FindNearbyDrivers(ctx context.Context, lat, lng float64, radiusKM float64) ([]string, error)
	TryLockDriver(ctx context.Context, driverID, rideId string) (bool, error)
}
