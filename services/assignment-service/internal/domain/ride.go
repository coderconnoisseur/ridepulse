package domain

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
