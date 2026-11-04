package pricing

import (
	"math"
	"testing"

	"ridepulse/pkg/events"
)

func priceBetween(pickup, drop events.Location) float64 {
	return ComputePrice(events.RideRequestedEvent{Pickup: pickup, Drop: drop}).Price
}

func TestComputePriceChargesRealDistance(t *testing.T) {
	tests := []struct {
		name       string
		pickup     events.Location
		drop       events.Location
		distanceKM float64
	}{
		{"same point costs the base fare", events.Location{Lat: 12.9, Lng: 77.6}, events.Location{Lat: 12.9, Lng: 77.6}, 0},
		{"0.1 degree of latitude is about 11 km", events.Location{Lat: 12.9, Lng: 77.6}, events.Location{Lat: 13.0, Lng: 77.6}, 11.12},
		// A degree of longitude shrinks with latitude: 0.1 degree is 11.12 km
		// at the equator but only 5.56 km at 60 degrees north.
		{"longitude shrinks away from the equator", events.Location{Lat: 60, Lng: 10}, events.Location{Lat: 60, Lng: 10.1}, 5.56},
		{"short trip across the antimeridian", events.Location{Lat: 0, Lng: 179.95}, events.Location{Lat: 0, Lng: -179.95}, 11.12},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := priceBetween(tt.pickup, tt.drop)
			want := baseFareINR + tt.distanceKM*perKMINR
			if math.Abs(got-want) > 0.5 {
				t.Fatalf("price = %.2f, want about %.2f", got, want)
			}
		})
	}
}

func TestComputePriceCopiesRideFields(t *testing.T) {
	pickup := events.Location{Lat: 12.9, Lng: 77.6}
	got := ComputePrice(events.RideRequestedEvent{RideID: "r1", Pickup: pickup, RequestedAt: 42})

	if got.Version != events.Version || got.RideID != "r1" || got.Pickup != pickup || got.RequestedAt != 42 {
		t.Fatalf("event fields not carried over: %+v", got)
	}
	if got.Currency != "INR" || got.SurgeMultiplier != 1.0 {
		t.Fatalf("currency = %q, surge = %v", got.Currency, got.SurgeMultiplier)
	}
}
