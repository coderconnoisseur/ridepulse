package matching

import (
	"context"
	"errors"
	"testing"
	"time"

	"ridepulse/pkg/events"
)

type fakeRepo struct {
	driverID string
	replayed bool
	err      error
	check    func(ctx context.Context)
}

func (f *fakeRepo) ClaimNearestDriver(ctx context.Context, _ string, _, _, _ float64) (string, bool, error) {
	if f.check != nil {
		f.check(ctx)
	}
	return f.driverID, f.replayed, f.err
}

func TestMatchReturnsClaimedDriver(t *testing.T) {
	svc := New(&fakeRepo{driverID: "driver-7"}, time.Second)

	got, err := svc.Match(context.Background(), events.RidePricedEvent{RideID: "ride-1", RequestedAt: 42})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if got.RideID != "ride-1" || got.DriverID != "driver-7" || got.RequestedAt != 42 || got.Version != events.Version {
		t.Fatalf("Match = %+v", got)
	}
}

func TestMatchReplayReturnsExistingDriver(t *testing.T) {
	svc := New(&fakeRepo{driverID: "driver-7", replayed: true}, time.Second)

	got, err := svc.Match(context.Background(), events.RidePricedEvent{RideID: "ride-1"})
	if err != nil || got.DriverID != "driver-7" {
		t.Fatalf("Match = %+v, %v", got, err)
	}
}

func TestMatchNoDriverReturnsErrNoDriversAvailable(t *testing.T) {
	svc := New(&fakeRepo{}, time.Second)

	_, err := svc.Match(context.Background(), events.RidePricedEvent{RideID: "ride-1"})
	if !errors.Is(err, ErrNoDriversAvailable) {
		t.Fatalf("err = %v, want ErrNoDriversAvailable", err)
	}
}

func TestMatchRepoErrorIsNotABusinessOutcome(t *testing.T) {
	boom := errors.New("redis down")
	svc := New(&fakeRepo{err: boom}, time.Second)

	_, err := svc.Match(context.Background(), events.RidePricedEvent{RideID: "ride-1"})
	if !errors.Is(err, boom) || errors.Is(err, ErrNoDriversAvailable) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestMatchClaimSurvivesCancelAndHasTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // shutdown began before the claim ran

	var claimErr error
	var hasDeadline bool
	repo := &fakeRepo{driverID: "driver-1", check: func(c context.Context) {
		claimErr = c.Err()
		_, hasDeadline = c.Deadline()
	}}

	if _, err := New(repo, time.Second).Match(ctx, events.RidePricedEvent{RideID: "ride-1"}); err != nil {
		t.Fatalf("Match: %v", err)
	}
	if claimErr != nil {
		t.Fatalf("claim context cancelled with caller: %v", claimErr)
	}
	if !hasDeadline {
		t.Fatal("claim context has no timeout")
	}
}
