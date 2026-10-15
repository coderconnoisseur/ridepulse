package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"ridepulse/services/matching-service/internal/ports"
)

func newTestRepo(t *testing.T) (*DriverRepository, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	repo := NewDriverRepository(mr.Addr(), 10)
	t.Cleanup(func() { _ = repo.Close() })
	return repo, mr
}

func TestTryLockDriverRefusesAssignedDriver(t *testing.T) {
	repo, mr := newTestRepo(t)
	ctx := context.Background()

	if err := mr.Set("driver:assigned:d1", "ride-0"); err != nil {
		t.Fatal(err)
	}

	ok, err := repo.TryLockDriver(ctx, "d1", "ride-1")
	if err != nil {
		t.Fatalf("TryLockDriver: %v", err)
	}
	if ok {
		t.Fatal("lock granted for an assigned driver")
	}
	if mr.Exists("driver:lock:d1") {
		t.Fatal("lock key written for an assigned driver")
	}
}

func TestTryLockDriverLocksFreeDriverOnce(t *testing.T) {
	repo, mr := newTestRepo(t)
	ctx := context.Background()

	ok, err := repo.TryLockDriver(ctx, "d1", "ride-1")
	if err != nil || !ok {
		t.Fatalf("first lock: ok=%v err=%v", ok, err)
	}
	if got, _ := mr.Get("driver:lock:d1"); got != "ride-1" {
		t.Fatalf("lock value = %q, want ride-1", got)
	}
	if ttl := mr.TTL("driver:lock:d1"); ttl <= 0 || ttl > 3*time.Second {
		t.Fatalf("lock ttl = %v, want (0, 3s]", ttl)
	}

	ok, err = repo.TryLockDriver(ctx, "d1", "ride-2")
	if err != nil {
		t.Fatalf("second lock: %v", err)
	}
	if ok {
		t.Fatal("second lock granted while first is held")
	}
}

func TestAssignDriverRecordsAssignment(t *testing.T) {
	repo, mr := newTestRepo(t)
	ctx := context.Background()

	if err := repo.client.GeoAdd(ctx, "drivers:locations",
		&redis.GeoLocation{Name: "d1", Longitude: 72.8, Latitude: 19.0},
		&redis.GeoLocation{Name: "d2", Longitude: 72.81, Latitude: 19.01},
	).Err(); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.TryLockDriver(ctx, "d1", "ride-1"); err != nil || !ok {
		t.Fatalf("lock: ok=%v err=%v", ok, err)
	}

	got, err := repo.AssignDriver(ctx, "d1", "ride-1")
	if err != nil {
		t.Fatalf("AssignDriver: %v", err)
	}
	if got != "d1" {
		t.Fatalf("assigned driver = %q, want d1", got)
	}

	if v, _ := mr.Get("driver:assigned:d1"); v != "ride-1" {
		t.Fatalf("driver:assigned:d1 = %q, want ride-1", v)
	}
	if ttl := mr.TTL("driver:assigned:d1"); ttl != 10*time.Minute {
		t.Fatalf("driver:assigned ttl = %v, want 10m", ttl)
	}
	if v, _ := mr.Get("ride:driver:ride-1"); v != "d1" {
		t.Fatalf("ride:driver:ride-1 = %q, want d1", v)
	}
	if ttl := mr.TTL("ride:driver:ride-1"); ttl != time.Hour {
		t.Fatalf("ride:driver ttl = %v, want 1h", ttl)
	}
	if mr.Exists("driver:lock:d1") {
		t.Fatal("lock not deleted after assignment")
	}
	members, err := mr.ZMembers("drivers:locations")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0] != "d2" {
		t.Fatalf("drivers:locations = %v, want [d2]", members)
	}

	driverID, found, err := repo.GetRideDriver(ctx, "ride-1")
	if err != nil || !found || driverID != "d1" {
		t.Fatalf("GetRideDriver = (%q, %v, %v), want (d1, true, nil)", driverID, found, err)
	}
}

func TestAssignDriverWithForeignLockReturnsErrLockLost(t *testing.T) {
	repo, mr := newTestRepo(t)
	ctx := context.Background()

	// Our lock expired and another ride took the driver.
	if err := mr.Set("driver:lock:d1", "ride-other"); err != nil {
		t.Fatal(err)
	}

	_, err := repo.AssignDriver(ctx, "d1", "ride-1")
	if !errors.Is(err, ports.ErrLockLost) {
		t.Fatalf("err = %v, want ErrLockLost", err)
	}
	if v, _ := mr.Get("driver:lock:d1"); v != "ride-other" {
		t.Fatalf("foreign lock touched: %q", v)
	}
	if mr.Exists("driver:assigned:d1") || mr.Exists("ride:driver:ride-1") {
		t.Fatal("assignment written despite lost lock")
	}
}

func TestAssignDriverWithNoLockReturnsErrLockLost(t *testing.T) {
	repo, _ := newTestRepo(t)

	_, err := repo.AssignDriver(context.Background(), "d1", "ride-1")
	if !errors.Is(err, ports.ErrLockLost) {
		t.Fatalf("err = %v, want ErrLockLost", err)
	}
}

func TestAssignDriverSecondDriverForSameRideReturnsFirst(t *testing.T) {
	repo, mr := newTestRepo(t)
	ctx := context.Background()

	if ok, err := repo.TryLockDriver(ctx, "d1", "ride-1"); err != nil || !ok {
		t.Fatalf("lock d1: ok=%v err=%v", ok, err)
	}
	if _, err := repo.AssignDriver(ctx, "d1", "ride-1"); err != nil {
		t.Fatalf("assign d1: %v", err)
	}

	// A redelivery of the same ride locked a different driver.
	if ok, err := repo.TryLockDriver(ctx, "d2", "ride-1"); err != nil || !ok {
		t.Fatalf("lock d2: ok=%v err=%v", ok, err)
	}
	got, err := repo.AssignDriver(ctx, "d2", "ride-1")
	if err != nil {
		t.Fatalf("assign d2: %v", err)
	}
	if got != "d1" {
		t.Fatalf("assigned driver = %q, want d1 (first assignment)", got)
	}
	if mr.Exists("driver:lock:d2") {
		t.Fatal("d2 lock not freed")
	}
	if mr.Exists("driver:assigned:d2") {
		t.Fatal("d2 marked assigned")
	}
	if v, _ := mr.Get("ride:driver:ride-1"); v != "d1" {
		t.Fatalf("ride:driver:ride-1 = %q, want d1", v)
	}
}

func TestGetRideDriverNotFound(t *testing.T) {
	repo, _ := newTestRepo(t)

	driverID, found, err := repo.GetRideDriver(context.Background(), "ride-x")
	if err != nil || found || driverID != "" {
		t.Fatalf("GetRideDriver = (%q, %v, %v), want (\"\", false, nil)", driverID, found, err)
	}
}
