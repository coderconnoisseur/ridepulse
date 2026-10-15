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

// RideUnmatchedEvent is published when matching ends in a business failure
// (no driver found, or the match deadline expired).
type RideUnmatchedEvent struct{
	RideID string
	Reason string // "no_drivers" or "match_deadline"
	RequestedAt int64 // UnixNano, carried through from RidePricedEvent
	FailedAt int64 // UnixNano
}
