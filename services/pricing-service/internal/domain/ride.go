//events
//will describe how our events look like
//basically equivalent to fastapi pydantic models for events
package domain

type Location struct {
	Lat float64
	Lng float64
}
type RideRequestedEvent struct{
	RideId string
	Pickup Location
	Drop Location
	RequestedAt int64 // UnixNano, propagated end-to-end for latency measurement
}
type RidePricedEvent struct{
	RideId string
	Price float64
	Currency string
	SurgeMultiplier float64
	Pickup Location
	RequestedAt int64 // carried through from RideRequestedEvent
}