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

// RideUnmatchedEvent is published by matching-service on ride.unmatched when
// a ride could not be matched. FailedAt is a UnixNano timestamp.
type RideUnmatchedEvent struct {
	RideID      string
	Reason      string
	RequestedAt int64
	FailedAt    int64
}

// Assignment is what we store for each successfully matched ride.
type Assignment struct {
	RideID    string `json:"ride_id"`
	DriverID  string `json:"driver_id"`
	MatchedAT int64  `json:"matched_at"`
	LatencyMs int64  `json:"latency_ms"`
}

// Ride outcome statuses reported by the store and the HTTP API.
const (
	StatusMatched   = "matched"
	StatusUnmatched = "unmatched"
)

// Outcome is the terminal result recorded for a ride: the assignment when
// matched, or the reason and failure time when unmatched.
type Outcome struct {
	RideID     string
	Status     string
	Assignment Assignment
	Reason     string
	FailedAt   int64
}
