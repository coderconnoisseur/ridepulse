package domain

type Location struct{
	Lat float64
	Lng float64
}


type RidePricedEvent struct{
	RideID string
	Price float64
	Currency string
	SurgeMultiplier float64
	Pickup Location
	RequestedAt int64 // UnixNano, propagated end-to-end for latency measurement
}

type RideMatchedEvent struct{
	RideID string
	DriverID string
	MatchedAT int64
	RequestedAt int64 // carried through from RideRequestedEvent
}