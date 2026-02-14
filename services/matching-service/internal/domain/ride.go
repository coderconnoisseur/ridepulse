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
}

type RideMatchedEvent struct{
	RideID string
	DriverID string 
	MatchedAT int64
}