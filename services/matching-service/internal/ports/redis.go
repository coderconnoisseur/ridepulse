package ports

import (
	"context"
	"errors"
)

// ErrLockLost is returned by DriverRepository.AssignDriver when the caller no
// longer holds driver:lock:<driverID> (it expired or was taken by another
// ride between TryLockDriver and AssignDriver).
var ErrLockLost = errors.New("driver lock lost before assignment")

type DriverRepository interface {
	FindNearbyDrivers(ctx context.Context, lat, lng float64, radiusKM float64) ([]string, error)
	TryLockDriver(ctx context.Context, driverID, rideId string) (bool, error)
	UnlockDriver(ctx context.Context, driverID, rideID string) error
	// AssignDriver atomically converts our lock on driverID into an
	// assignment for rideID. If rideID was already assigned, our lock is
	// released and the existing driver is returned instead.
	AssignDriver(ctx context.Context, driverID, rideID string) (assignedDriverID string, err error)
	// GetRideDriver returns the driver already assigned to rideID, if any.
	GetRideDriver(ctx context.Context, rideID string) (driverID string, found bool, err error)
}
