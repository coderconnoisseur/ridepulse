package ports

import "context"

// Claim is the outcome of asking for a driver. An empty DriverID means the ride
// has no driver, and that result is final for the ride.
type Claim struct {
	DriverID  string
	ClaimedAt int64 // Unix seconds when the driver was first claimed
	Replayed  bool  // the ride already had this driver
}

type DriverRepository interface {
	// ClaimNearestDriver atomically decides the ride's outcome once: the
	// nearest free driver within radiusKM, or no driver if none is free. Later
	// calls for the same rideID return that same outcome and change nothing.
	ClaimNearestDriver(ctx context.Context, rideID string, lat, lng, radiusKM float64) (Claim, error)
}
