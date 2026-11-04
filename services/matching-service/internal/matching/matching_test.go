package matching

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"ridepulse/pkg/events"
	"ridepulse/services/matching-service/internal/ports"
)

// TestShuffleWithinTiersPreservesTierBoundaries verifies the core invariant:
// after shuffling, every element originally in tier N is still in tier N.
// If this ever fails, the shuffle is no longer preserving nearest-first
// semantics and the fix is silently regressed.
func TestShuffleWithinTiersPreservesTierBoundaries(t *testing.T) {
	const tierSize = 10
	const n = 20

	// Original order encodes the nearest-first index in each element,
	// so we can check tier membership after shuffling.
	drivers := make([]string, n)
	for i := 0; i < n; i++ {
		drivers[i] = "driver-" + strconv.Itoa(i)
	}

	// Run many trials so any tier-crossing bug is very likely to surface.
	for trial := 0; trial < 200; trial++ {
		copy := append([]string(nil), drivers...)
		shuffleWithinTiers(copy, tierSize)

		// Tier 1 (indices 0..9) must contain the driver-0 through driver-9 set.
		tier1 := stringSet(copy[:tierSize])
		for i := 0; i < tierSize; i++ {
			want := "driver-" + strconv.Itoa(i)
			if _, ok := tier1[want]; !ok {
				t.Fatalf("trial %d: %s escaped tier 1: got %v", trial, want, copy[:tierSize])
			}
		}

		// Tier 2 (indices 10..19) must contain the driver-10 through driver-19 set.
		tier2 := stringSet(copy[tierSize:])
		for i := tierSize; i < n; i++ {
			want := "driver-" + strconv.Itoa(i)
			if _, ok := tier2[want]; !ok {
				t.Fatalf("trial %d: %s escaped tier 2: got %v", trial, want, copy[tierSize:])
			}
		}
	}
}

// TestShuffleWithinTiersActuallyShuffles guards against the tier size being
// so large (or so small) that the "shuffle" is effectively identity. If the
// output equals the input across many trials, we've silently lost the
// contention-spreading property.
func TestShuffleWithinTiersActuallyShuffles(t *testing.T) {
	const tierSize = 10
	const n = 20

	drivers := make([]string, n)
	for i := 0; i < n; i++ {
		drivers[i] = "driver-" + strconv.Itoa(i)
	}

	permutedAtLeastOnce := false
	for trial := 0; trial < 100; trial++ {
		copy := append([]string(nil), drivers...)
		shuffleWithinTiers(copy, tierSize)
		if !equal(copy, drivers) {
			permutedAtLeastOnce = true
			break
		}
	}
	if !permutedAtLeastOnce {
		t.Fatalf("shuffleWithinTiers produced identity in 100 trials — is it actually shuffling?")
	}
}

// TestShuffleWithinTiersHandlesRaggedLastTier verifies the last (short)
// tier when len(drivers) is not a clean multiple of tierSize.
func TestShuffleWithinTiersHandlesRaggedLastTier(t *testing.T) {
	drivers := []string{"a", "b", "c", "d", "e", "f", "g"} // 7 items
	shuffleWithinTiers(drivers, 5)                         // → tiers of 5 and 2

	seen := stringSet(drivers)
	for _, want := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("element %q missing after shuffle: %v", want, drivers)
		}
	}
}

// TestShuffleWithinTiersTrivialTierSize verifies tierSize < 2 is a no-op,
// so we don't accidentally cause a "shuffle 1-element slice" pointless
// call in a hot path if someone tunes tierSize down.
func TestShuffleWithinTiersTrivialTierSize(t *testing.T) {
	drivers := []string{"a", "b", "c"}
	shuffleWithinTiers(drivers, 1)
	if !equal(drivers, []string{"a", "b", "c"}) {
		t.Fatalf("tierSize=1 should be a no-op, got %v", drivers)
	}
	shuffleWithinTiers(drivers, 0)
	if !equal(drivers, []string{"a", "b", "c"}) {
		t.Fatalf("tierSize=0 should be a no-op, got %v", drivers)
	}
}

func stringSet(s []string) map[string]struct{} {
	out := make(map[string]struct{}, len(s))
	for _, v := range s {
		out[v] = struct{}{}
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// fakeRepo is an in-memory ports.DriverRepository for matcher tests.
// Every lock attempt succeeds; AssignDriver is delegated to assignFn so each
// test can script its outcome.
type fakeRepo struct {
	drivers []string

	mu         sync.Mutex
	rideDriver map[string]string
	lockCalls  []string
	assigns    []string
	unlocks    []string
	assignFn   func(call int, driverID, rideID string) (string, error)
}

func (f *fakeRepo) FindNearbyDrivers(ctx context.Context, lat, lng, radiusKM float64) ([]string, error) {
	return append([]string(nil), f.drivers...), nil
}

func (f *fakeRepo) TryLockDriver(ctx context.Context, driverID, rideID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lockCalls = append(f.lockCalls, driverID)
	return true, nil
}

func (f *fakeRepo) UnlockDriver(ctx context.Context, driverID, rideID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unlocks = append(f.unlocks, driverID)
	return nil
}

func (f *fakeRepo) AssignDriver(ctx context.Context, driverID, rideID string) (string, error) {
	f.mu.Lock()
	call := len(f.assigns)
	f.assigns = append(f.assigns, driverID)
	fn := f.assignFn
	f.mu.Unlock()
	if fn == nil {
		return driverID, nil
	}
	return fn(call, driverID, rideID)
}

func (f *fakeRepo) GetRideDriver(ctx context.Context, rideID string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.rideDriver[rideID]
	return d, ok, nil
}

func (f *fakeRepo) snapshot() (locks, assigns, unlocks []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.lockCalls...),
		append([]string(nil), f.assigns...),
		append([]string(nil), f.unlocks...)
}

func driverIDs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "driver-" + strconv.Itoa(i)
	}
	return out
}

func TestMatchReplaysExistingAssignment(t *testing.T) {
	repo := &fakeRepo{
		drivers:    driverIDs(10),
		rideDriver: map[string]string{"ride-1": "driver-7"},
	}
	svc := New(repo)

	got, err := svc.Match(context.Background(), events.RidePricedEvent{RideID: "ride-1", RequestedAt: 42})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if got.RideID != "ride-1" || got.DriverID != "driver-7" || got.RequestedAt != 42 {
		t.Fatalf("Match = %+v, want ride-1/driver-7/RequestedAt 42", got)
	}
	locks, assigns, _ := repo.snapshot()
	if len(locks) != 0 || len(assigns) != 0 {
		t.Fatalf("replay touched locks=%v assigns=%v, want none", locks, assigns)
	}
}

func TestMatchLockLostOnFirstBatchMovesToNextBatch(t *testing.T) {
	repo := &fakeRepo{
		drivers: driverIDs(10),
		assignFn: func(call int, driverID, rideID string) (string, error) {
			if call == 0 {
				return "", ports.ErrLockLost
			}
			return driverID, nil
		},
	}
	svc := New(repo)

	got, err := svc.Match(context.Background(), events.RidePricedEvent{RideID: "ride-1"})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	_, assigns, _ := repo.snapshot()
	if len(assigns) != 2 {
		t.Fatalf("assign calls = %v, want 2", assigns)
	}
	// Batches are disjoint slices of the driver list, so a different
	// driver proves the second assign came from the next batch.
	if assigns[0] == assigns[1] {
		t.Fatalf("both assigns used %q; expected a driver from the next batch", assigns[0])
	}
	if got.DriverID != assigns[1] {
		t.Fatalf("matched %q, want second assigned driver %q", got.DriverID, assigns[1])
	}
}

func TestMatchUsesDriverReturnedByAssign(t *testing.T) {
	repo := &fakeRepo{
		drivers: driverIDs(5),
		assignFn: func(call int, driverID, rideID string) (string, error) {
			return "driver-earlier", nil
		},
	}
	svc := New(repo)

	got, err := svc.Match(context.Background(), events.RidePricedEvent{RideID: "ride-1"})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if got.DriverID != "driver-earlier" {
		t.Fatalf("matched %q, want driver-earlier", got.DriverID)
	}
}

func TestMatchAssignErrorReleasesLockAndFails(t *testing.T) {
	boom := errors.New("redis down")
	repo := &fakeRepo{
		drivers: driverIDs(5),
		assignFn: func(call int, driverID, rideID string) (string, error) {
			return "", boom
		},
	}
	svc := New(repo)

	_, err := svc.Match(context.Background(), events.RidePricedEvent{RideID: "ride-1"})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	_, assigns, unlocks := repo.snapshot()
	if len(assigns) != 1 {
		t.Fatalf("assign calls = %v, want 1", assigns)
	}
	found := false
	for _, u := range unlocks {
		if u == assigns[0] {
			found = true
		}
	}
	if !found {
		t.Fatalf("winner %q not unlocked; unlocks=%v", assigns[0], unlocks)
	}
}

func TestMatchNoDriversReturnsErrNoDriversAvailable(t *testing.T) {
	svc := New(&fakeRepo{})

	_, err := svc.Match(context.Background(), events.RidePricedEvent{RideID: "ride-1"})
	if !errors.Is(err, ErrNoDriversAvailable) {
		t.Fatalf("err = %v, want ErrNoDriversAvailable", err)
	}
}

func TestMatchAllAssignsLockLostReturnsErrNoDriversAvailable(t *testing.T) {
	repo := &fakeRepo{
		drivers: driverIDs(10),
		assignFn: func(call int, driverID, rideID string) (string, error) {
			return "", ports.ErrLockLost
		},
	}
	svc := New(repo)

	_, err := svc.Match(context.Background(), events.RidePricedEvent{RideID: "ride-1"})
	if !errors.Is(err, ErrNoDriversAvailable) {
		t.Fatalf("err = %v, want ErrNoDriversAvailable", err)
	}
}

func TestMatchAssignIgnoresExpiredMatchDeadline(t *testing.T) {
	var assignCtxErr error
	repo := &fakeRepo{drivers: driverIDs(5)}
	ctx, cancel := context.WithCancel(context.Background())
	repo.assignFn = func(call int, driverID, rideID string) (string, error) {
		// Match deadline fires while the assign is in flight.
		cancel()
		return driverID, nil
	}
	svc := New(repo)
	svc.repo = assignCtxRecorder{fakeRepo: repo, errOut: &assignCtxErr}

	got, err := svc.Match(ctx, events.RidePricedEvent{RideID: "ride-1"})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if got.DriverID == "" {
		t.Fatal("no driver returned")
	}
	if assignCtxErr != nil {
		t.Fatalf("assign context cancelled with match ctx: %v", assignCtxErr)
	}
}

// assignCtxRecorder records whether the AssignDriver context was cancelled
// after the fake's assignFn ran (which cancels the parent match context).
type assignCtxRecorder struct {
	*fakeRepo
	errOut *error
}

func (a assignCtxRecorder) AssignDriver(ctx context.Context, driverID, rideID string) (string, error) {
	id, err := a.fakeRepo.AssignDriver(ctx, driverID, rideID)
	*a.errOut = ctx.Err()
	return id, err
}
