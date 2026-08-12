package domain

type Location struct {
	Lat float64
	Lng float64
}

// RideRequestedEvent is stamped by api-gateway when a POST /rides is
// accepted. RequestedAt (UnixNano) is propagated by pricing-service and
// matching-service into their respective events so assignment-service
// can measure end-to-end latency at the terminal consumer.
type RideRequestedEvent struct {
	RideID      string
	Pickup      Location
	Drop        Location
	RequestedAt int64
}
