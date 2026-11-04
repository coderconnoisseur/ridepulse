// pricing logic
package pricing

import (
	"math"

	"ridepulse/pkg/events"
)

const (
	baseFareINR   = 50.0
	perKMINR      = 12.0
	earthRadiusKM = 6371.0
)

// ComputePrice builds the ride.priced event for a ride.requested event. The
// surge multiplier applies to the whole fare, and the price is in whole paise.
func ComputePrice(event events.RideRequestedEvent, surge float64) events.RidePricedEvent {
	fare := (baseFareINR + distanceKM(event.Pickup, event.Drop)*perKMINR) * surge
	return events.RidePricedEvent{
		Version:         events.Version,
		RideID:          event.RideID,
		Price:           math.Round(fare*100) / 100,
		Currency:        "INR",
		SurgeMultiplier: surge,
		Pickup:          event.Pickup,
		RequestedAt:     event.RequestedAt,
	}
}

// distanceKM returns the straight-line distance over the Earth's surface
// (haversine formula), not the road distance.
func distanceKM(a, b events.Location) float64 {
	lat1, lat2 := radians(a.Lat), radians(b.Lat)
	dLat, dLng := lat2-lat1, radians(b.Lng-a.Lng)
	h := math.Pow(math.Sin(dLat/2), 2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Pow(math.Sin(dLng/2), 2)
	return 2 * earthRadiusKM * math.Asin(math.Sqrt(h))
}

func radians(degrees float64) float64 {
	return degrees * math.Pi / 180
}
