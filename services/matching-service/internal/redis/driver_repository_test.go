package redis

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

const (
	pickupLat = 19.0
	pickupLng = 72.8
	radiusKM  = 50
)

func newTestRepo(t *testing.T) (*DriverRepository, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	repo := NewDriverRepository(mr.Addr(), 100)
	t.Cleanup(func() { _ = repo.Close() })
	return repo, mr
}

// addDriver places a driver about offsetDeg degrees of latitude from the pickup.
func addDriver(t *testing.T, repo *DriverRepository, id string, offsetDeg float64) {
	t.Helper()
	err := repo.client.GeoAdd(context.Background(), locationsKey,
		&redis.GeoLocation{Name: id, Longitude: pickupLng, Latitude: pickupLat + offsetDeg},
	).Err()
	if err != nil {
		t.Fatal(err)
	}
}

func claim(t *testing.T, repo *DriverRepository, rideID string) (string, bool) {
	t.Helper()
	id, replayed, err := repo.ClaimNearestDriver(context.Background(), rideID, pickupLat, pickupLng, radiusKM)
	if err != nil {
		t.Fatalf("ClaimNearestDriver(%s): %v", rideID, err)
	}
	return id, replayed
}

func TestClaimPicksNearestAndRecordsAssignment(t *testing.T) {
	repo, mr := newTestRepo(t)
	addDriver(t, repo, "far", 0.1)
	addDriver(t, repo, "near", 0.01)

	id, replayed := claim(t, repo, "ride-1")
	if id != "near" || replayed {
		t.Fatalf("claim = (%q, %v), want (near, false)", id, replayed)
	}
	if v, _ := mr.Get("driver:assigned:near"); v != "ride-1" {
		t.Fatalf("driver:assigned:near = %q, want ride-1", v)
	}
	if ttl := mr.TTL("driver:assigned:near"); ttl != 10*time.Minute {
		t.Fatalf("driver:assigned ttl = %v, want 10m", ttl)
	}
	if v, _ := mr.Get("ride:driver:ride-1"); v != "near" {
		t.Fatalf("ride:driver:ride-1 = %q, want near", v)
	}
	if ttl := mr.TTL("ride:driver:ride-1"); ttl != time.Hour {
		t.Fatalf("ride:driver ttl = %v, want 1h", ttl)
	}
	members, err := mr.ZMembers(locationsKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0] != "far" {
		t.Fatalf("drivers:locations = %v, want [far]", members)
	}
}

func TestClaimSkipsAssignedDriverStillInGeoSet(t *testing.T) {
	repo, mr := newTestRepo(t)
	addDriver(t, repo, "busy", 0.01)
	addDriver(t, repo, "free", 0.1)
	if err := mr.Set("driver:assigned:busy", "ride-0"); err != nil {
		t.Fatal(err)
	}

	if id, _ := claim(t, repo, "ride-1"); id != "free" {
		t.Fatalf("claimed %q, want free", id)
	}
	if v, _ := mr.Get("driver:assigned:busy"); v != "ride-0" {
		t.Fatalf("busy driver reassigned to %q", v)
	}
}

func TestClaimReplayReturnsSameDriverAndChangesNothing(t *testing.T) {
	repo, mr := newTestRepo(t)
	addDriver(t, repo, "d1", 0.01)
	addDriver(t, repo, "d2", 0.02)

	first, _ := claim(t, repo, "ride-1")
	again, replayed := claim(t, repo, "ride-1")
	if again != first || !replayed {
		t.Fatalf("replay = (%q, %v), want (%q, true)", again, replayed, first)
	}
	if mr.Exists("driver:assigned:d2") {
		t.Fatal("replay assigned a second driver")
	}
}

func TestClaimNoDriverInRange(t *testing.T) {
	repo, mr := newTestRepo(t)
	addDriver(t, repo, "far", 5) // about 550 km away

	id, replayed := claim(t, repo, "ride-1")
	if id != "" || replayed {
		t.Fatalf("claim = (%q, %v), want no driver", id, replayed)
	}
	if mr.Exists("ride:driver:ride-1") {
		t.Fatal("ride recorded without a driver")
	}
}

func TestClaimOnEmptyGeoSet(t *testing.T) {
	repo, _ := newTestRepo(t)

	if id, _ := claim(t, repo, "ride-1"); id != "" {
		t.Fatalf("claimed %q from an empty set", id)
	}
}

func TestConcurrentClaimsNeverShareADriver(t *testing.T) {
	repo, _ := newTestRepo(t)
	const drivers, rides = 10, 50
	for i := range drivers {
		addDriver(t, repo, fmt.Sprintf("d%d", i), float64(i)*0.001)
	}

	var mu sync.Mutex
	got := map[string]string{}
	var wg sync.WaitGroup
	for i := range rides {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rideID := fmt.Sprintf("ride-%d", i)
			id, _, err := repo.ClaimNearestDriver(context.Background(), rideID, pickupLat, pickupLng, radiusKM)
			if err != nil {
				t.Errorf("claim %s: %v", rideID, err)
				return
			}
			if id == "" {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if other, dup := got[id]; dup {
				t.Errorf("driver %s given to %s and %s", id, other, rideID)
			}
			got[id] = rideID
		}()
	}
	wg.Wait()
	if len(got) != drivers {
		t.Fatalf("matched %d drivers, want %d: every free driver should be claimed", len(got), drivers)
	}
}

func TestClaimHonoursContextDeadline(t *testing.T) {
	// A server that accepts connections and never replies.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()
	repo := NewDriverRepository(ln.Addr().String(), 1)
	defer repo.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err = repo.ClaimNearestDriver(ctx, "ride-1", pickupLat, pickupLng, radiusKM)
	if err == nil {
		t.Fatal("want an error from a server that never replies")
	}
	// The go-redis default read timeout is 3 s; the context must win well before it.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("claim took %s, want it bounded by the 50 ms context", elapsed)
	}
}
