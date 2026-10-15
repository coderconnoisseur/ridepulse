package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"ridepulse/services/assignment-service/internal/domain"
	"ridepulse/services/assignment-service/internal/store"
)

func get(mem *store.Memory, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	NewServer("", mem).Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestRideStatusShapes(t *testing.T) {
	mem := store.NewMemory()
	mem.PutMatched(domain.Assignment{RideID: "m1", DriverID: "d1", MatchedAT: 100, LatencyMs: 42})
	mem.PutUnmatched("u1", "no_drivers", 200)

	cases := []struct {
		path string
		want map[string]any
	}{
		{"/rides/m1", map[string]any{
			"ride_id": "m1", "status": "matched", "driver_id": "d1", "matched_at": 100.0, "latency_ms": 42.0,
		}},
		{"/rides/u1", map[string]any{
			"ride_id": "u1", "status": "unmatched", "reason": "no_drivers", "failed_at": 200.0,
		}},
	}
	for _, tc := range cases {
		rec := get(mem, tc.path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s code = %d", tc.path, rec.Code)
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s body = %v, want %v", tc.path, got, tc.want)
		}
	}

	if rec := get(mem, "/rides/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown code = %d, want 404", rec.Code)
	}
}

func TestAssignmentsAndCountUnchanged(t *testing.T) {
	mem := store.NewMemory()
	mem.PutMatched(domain.Assignment{RideID: "m1", DriverID: "d1"})
	mem.PutUnmatched("u1", "no_drivers", 200)

	if rec := get(mem, "/assignments/m1"); rec.Code != http.StatusOK {
		t.Fatalf("assignments matched code = %d", rec.Code)
	}
	if rec := get(mem, "/assignments/u1"); rec.Code != http.StatusNotFound {
		t.Fatalf("assignments unmatched code = %d, want 404", rec.Code)
	}
	if rec := get(mem, "/count"); rec.Body.String() != "1\n" {
		t.Fatalf("count = %q, want 1", rec.Body.String())
	}
}
