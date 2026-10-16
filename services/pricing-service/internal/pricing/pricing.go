// pricing logic
package pricing

import (
	"math"

	"ridepulse/pkg/events"
)

// ComputePrice builds the ride.priced event for a ride.requested event.
func ComputePrice(event events.RideRequestedEvent) events.RidePricedEvent {
	baseFare := 50.0 // for MVP
	distanceKM := math.Hypot(
		event.Pickup.Lat-event.Drop.Lat,
		event.Pickup.Lng-event.Drop.Lng,
	) * 10 // scale factor
	price := baseFare + (distanceKM * 12) // 12 per km until the MVP develops
	return events.RidePricedEvent{
		Version:         events.Version,
		RideID:          event.RideID,
		Price:           price,
		Currency:        "INR",
		SurgeMultiplier: 1.0, // until the MVP develops
		Pickup:          event.Pickup,
		RequestedAt:     event.RequestedAt,
	}
}
