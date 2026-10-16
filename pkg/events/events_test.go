package events

import (
	"encoding/json"
	"reflect"
	"testing"
)

// roundTrip marshals v, checks the wire keys, unmarshals into a fresh value of
// the same type and compares. want is the expected JSON object.
func roundTrip[T any](t *testing.T, v T, wantKeys []string) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if len(obj) != len(wantKeys) {
		t.Errorf("got %d keys %v, want %d keys %v", len(obj), obj, len(wantKeys), wantKeys)
	}
	for _, k := range wantKeys {
		if _, ok := obj[k]; !ok {
			t.Errorf("missing wire key %q in %s", k, raw)
		}
	}
	var back T
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(back, v) {
		t.Errorf("round trip mismatch:\n got  %+v\n want %+v", back, v)
	}
}

func TestRideRequestedRoundTrip(t *testing.T) {
	roundTrip(t, RideRequestedEvent{
		Version: Version, RideID: "r1",
		Pickup: Location{Lat: 0, Lng: 0}, Drop: Location{Lat: 12.9, Lng: 77.6},
		RequestedAt: 42,
	}, []string{"v", "ride_id", "pickup", "drop", "requested_at"})
}

func TestRidePricedRoundTrip(t *testing.T) {
	roundTrip(t, RidePricedEvent{
		Version: Version, RideID: "r1", Price: 88.5, Currency: "INR",
		SurgeMultiplier: 1, Pickup: Location{Lat: 1, Lng: 2}, RequestedAt: 42,
	}, []string{"v", "ride_id", "price", "currency", "surge_multiplier", "pickup", "requested_at"})
}

func TestRideMatchedRoundTrip(t *testing.T) {
	roundTrip(t, RideMatchedEvent{
		Version: Version, RideID: "r1", DriverID: "d1", MatchedAt: 7, RequestedAt: 42,
	}, []string{"v", "ride_id", "driver_id", "matched_at", "requested_at"})
}

func TestRideUnmatchedRoundTrip(t *testing.T) {
	roundTrip(t, RideUnmatchedEvent{
		Version: Version, RideID: "r1", Reason: ReasonNoDrivers, RequestedAt: 42, FailedAt: 99,
	}, []string{"v", "ride_id", "reason", "requested_at", "failed_at"})
}

func TestVersionIsOneOnTheWire(t *testing.T) {
	raw, _ := json.Marshal(RideMatchedEvent{Version: Version})
	var obj map[string]any
	_ = json.Unmarshal(raw, &obj)
	if obj["v"] != float64(1) {
		t.Fatalf("v = %v, want 1", obj["v"])
	}
}
