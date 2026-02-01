package domain

type Location struct {
	Lat float64
	Lng float64
}

type RideRequestedEvent struct {
	RideID string
	Pickup Location
	Drop   Location
}
