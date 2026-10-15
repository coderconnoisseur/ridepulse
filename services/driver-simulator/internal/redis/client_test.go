package redis

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

func newTestClient(t *testing.T) (*Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := New(mr.Addr())
	t.Cleanup(func() { _ = c.Close() })
	return c, mr
}

func inGeoSet(mr *miniredis.Miniredis, id string) bool {
	members, _ := mr.ZMembers(locationsKey)
	for _, m := range members {
		if m == id {
			return true
		}
	}
	return false
}

func TestUpdateWhileUnassignedAddsToGeoSet(t *testing.T) {
	c, mr := newTestClient(t)

	ride, err := c.UpdateDriverLocation(context.Background(), "d1", 12.9, 77.6)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if ride != "" {
		t.Fatalf("rideID = %q, want empty", ride)
	}
	if !inGeoSet(mr, "d1") {
		t.Fatal("driver not in geo set")
	}
}

func TestUpdateWhileAssignedReturnsRideAndSkipsGeoAdd(t *testing.T) {
	c, mr := newTestClient(t)
	if err := mr.Set(assignedKeyPrefix+"d1", "ride-1"); err != nil {
		t.Fatal(err)
	}

	ride, err := c.UpdateDriverLocation(context.Background(), "d1", 12.9, 77.6)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if ride != "ride-1" {
		t.Fatalf("rideID = %q, want ride-1", ride)
	}
	if inGeoSet(mr, "d1") {
		t.Fatal("assigned driver must not be added to geo set")
	}
}

func TestReleaseWithWrongRideIsNoop(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()
	if err := mr.Set(assignedKeyPrefix+"d1", "ride-1"); err != nil {
		t.Fatal(err)
	}

	if err := c.ReleaseDriver(ctx, "d1", "other-ride"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got, _ := mr.Get(assignedKeyPrefix + "d1"); got != "ride-1" {
		t.Fatalf("assignment = %q, want ride-1 untouched", got)
	}
}

func TestReleaseWithRightRideReaddsDriver(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()
	if err := mr.Set(assignedKeyPrefix+"d1", "ride-1"); err != nil {
		t.Fatal(err)
	}

	if err := c.ReleaseDriver(ctx, "d1", "ride-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if mr.Exists(assignedKeyPrefix + "d1") {
		t.Fatal("assignment should be deleted")
	}

	ride, err := c.UpdateDriverLocation(ctx, "d1", 12.9, 77.6)
	if err != nil || ride != "" {
		t.Fatalf("update after release = (%q, %v), want empty and nil", ride, err)
	}
	if !inGeoSet(mr, "d1") {
		t.Fatal("driver not re-added to geo set")
	}
}
