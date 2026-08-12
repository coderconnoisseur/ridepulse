package domain

// RideMatchedEvent is the terminal event in the RidePulse pipeline. It is
// consumed by the assignment-service from Kafka topic ride.matched.
//
// RequestedAt is the UnixNano timestamp stamped by api-gateway when the
// original HTTP POST /rides was received. It is propagated through the
// pricing and matching services and used here to compute end-to-end
// latency for the entire request.
type RideMatchedEvent struct {
	RideID      string
	DriverID    string
	MatchedAT   int64
	RequestedAt int64
}

// Assignment is what we store for each successfully matched ride.
type Assignment struct {
	RideID    string `json:"ride_id"`
	DriverID  string `json:"driver_id"`
	MatchedAT int64  `json:"matched_at"`
	LatencyMs int64  `json:"latency_ms"`
}
