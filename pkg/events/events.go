// Package events defines the JSON contracts shared by every RidePulse service
// that produces or consumes a Kafka topic. Each service imports these types
// instead of keeping its own copy, so a field rename shows up as a compile
// error rather than as silently empty values on the wire.
//
// Wire format: snake_case JSON keys and a leading "v" version field. Bump
// Version only for incompatible changes; consumers that cannot handle a
// version must dead-letter the message instead of guessing.
package events

// Version is the contract version written into every event produced by this
// module.
const Version = 1

// Topic names. Dead-letter topics are "<topic>.dlq".
const (
	TopicRideRequested = "ride.requested"
	TopicRidePriced    = "ride.priced"
	TopicRideMatched   = "ride.matched"
	TopicRideUnmatched = "ride.unmatched"
)

// Reasons carried by RideUnmatchedEvent.Reason.
const (
	ReasonNoDrivers     = "no_drivers"
	ReasonMatchDeadline = "match_deadline"
)

// Location is a WGS84 coordinate in degrees.
type Location struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// RideRequestedEvent is published by api-gateway when POST /rides is accepted.
// RequestedAt is UnixNano and is propagated to every later event so the
// terminal consumer can measure end-to-end latency.
type RideRequestedEvent struct {
	Version     int      `json:"v"`
	RideID      string   `json:"ride_id"`
	Pickup      Location `json:"pickup"`
	Drop        Location `json:"drop"`
	RequestedAt int64    `json:"requested_at"`
}

// RidePricedEvent is published by pricing-service after a fare is computed.
type RidePricedEvent struct {
	Version         int      `json:"v"`
	RideID          string   `json:"ride_id"`
	Price           float64  `json:"price"`
	Currency        string   `json:"currency"`
	SurgeMultiplier float64  `json:"surge_multiplier"`
	Pickup          Location `json:"pickup"`
	RequestedAt     int64    `json:"requested_at"`
}

// RideMatchedEvent is published by matching-service when a driver is assigned.
type RideMatchedEvent struct {
	Version     int    `json:"v"`
	RideID      string `json:"ride_id"`
	DriverID    string `json:"driver_id"`
	MatchedAt   int64  `json:"matched_at"`
	RequestedAt int64  `json:"requested_at"`
}

// RideUnmatchedEvent is published by matching-service when no driver could be
// assigned. FailedAt is UnixNano.
type RideUnmatchedEvent struct {
	Version     int    `json:"v"`
	RideID      string `json:"ride_id"`
	Reason      string `json:"reason"`
	RequestedAt int64  `json:"requested_at"`
	FailedAt    int64  `json:"failed_at"`
}
