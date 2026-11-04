package matching

import (
	"context"
	"errors"
	"testing"
	"time"

	"ridepulse/pkg/events"
	"ridepulse/services/matching-service/internal/ports"
)

type fakeRepo struct {
	claim ports.Claim
	err   error
	check func(ctx context.Context)
}

func (f *fakeRepo) ClaimNearestDriver(ctx context.Context, _ string, _, _, _ float64) (ports.Claim, error) {
	if f.check != nil {
		f.check(ctx)
	}
	return f.claim, f.err
}

func TestMatchReturnsClaimedDriverAndFare(t *testing.T) {
	svc := New(&fakeRepo{claim: ports.Claim{DriverID: "driver-7", ClaimedAt: 1000}}, time.Second)

	got, err := svc.Match(context.Background(), events.RidePricedEvent{
		RideID: "ride-1", RequestedAt: 42, Price: 134.18, Currency: "INR", SurgeMultiplier: 1.5,
	})
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	want := events.RideMatchedEvent{
		Version: events.Version, RideID: "ride-1", DriverID: "driver-7", MatchedAt: 1000, RequestedAt: 42,
		Price: 134.18, Currency: "INR", SurgeMultiplier: 1.5,
	}
	if got != want {
		t.Fatalf("Match = %+v, want %+v", got, want)
	}
}

func TestMatchReplayKeepsOriginalClaimTime(t *testing.T) {
	svc := New(&fakeRepo{claim: ports.Claim{DriverID: "driver-7", ClaimedAt: 1000, Replayed: true}}, time.Second)

	got, err := svc.Match(context.Background(), events.RidePricedEvent{RideID: "ride-1"})
	if err != nil || got.DriverID != "driver-7" || got.MatchedAt != 1000 {
		t.Fatalf("Match = %+v, %v; want driver-7 at the original claim time", got, err)
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
	repo := &fakeRepo{claim: ports.Claim{DriverID: "driver-1"}, check: func(c context.Context) {
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
