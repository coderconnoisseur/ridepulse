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
}
type RidePricedEvent struct{
	RideId string
	Price float64
	Currency string
	SurgeMultiplier float64
	Pickup Location
}