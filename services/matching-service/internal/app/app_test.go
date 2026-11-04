package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"ridepulse/pkg/events"
	"ridepulse/services/matching-service/internal/matching"
)

func TestIsBusinessFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		ctx  error
		want bool
	}{
		{"no drivers", matching.ErrNoDriversAvailable, nil, true},
		{"wrapped no drivers", fmt.Errorf("match: %w", matching.ErrNoDriversAvailable), nil, true},
		{"outer match deadline", context.DeadlineExceeded, context.DeadlineExceeded, true},
		{"inner deadline, match ctx alive", context.DeadlineExceeded, nil, false},
		{"redis error", errors.New("dial tcp: connection refused"), nil, false},
		{"canceled", context.Canceled, nil, false},
		{"assign timeout after outer deadline", fmt.Errorf("%w: %w", matching.ErrAssign, context.DeadlineExceeded), context.DeadlineExceeded, false},
	}
	for _, tc := range cases {
		if got := isBusinessFailure(tc.err, tc.ctx); got != tc.want {
			t.Errorf("%s: isBusinessFailure(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

func TestUnmatchedReason(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"no drivers", matching.ErrNoDriversAvailable, "no_drivers"},
		{"wrapped no drivers", fmt.Errorf("match: %w", matching.ErrNoDriversAvailable), "no_drivers"},
		{"deadline", context.DeadlineExceeded, "match_deadline"},
	}
	for _, tc := range cases {
		if got := unmatchedReason(tc.err); got != tc.want {
			t.Errorf("%s: unmatchedReason(%v) = %q, want %q", tc.name, tc.err, got, tc.want)
		}
	}
}

type fakeMatcher struct {
	match       events.RideMatchedEvent
	err         error
	waitForDone bool // block until the match ctx expires, like a real slow match
}

func (f fakeMatcher) Match(ctx context.Context, _ events.RidePricedEvent) (events.RideMatchedEvent, error) {
	if f.waitForDone {
		<-ctx.Done()
	}
	return f.match, f.err
}

type fakePublisher struct {
	matched      []events.RideMatchedEvent
	unmatched    []events.RideUnmatchedEvent
	unmatchedErr error
}

func (f *fakePublisher) PublishRideMatched(_ context.Context, e events.RideMatchedEvent) error {
	f.matched = append(f.matched, e)
	return nil
}

func (f *fakePublisher) PublishRideUnmatched(_ context.Context, e events.RideUnmatchedEvent) error {
	if f.unmatchedErr != nil {
		return f.unmatchedErr
	}
	f.unmatched = append(f.unmatched, e)
	return nil
}

func TestHandlerPublishesUnmatchedOnBusinessFailure(t *testing.T) {
	pub := &fakePublisher{}
	h := newHandler(fakeMatcher{err: matching.ErrNoDriversAvailable}, pub, time.Second)
	before := time.Now().UnixNano()
	if err := h(context.Background(), events.RidePricedEvent{RideID: "r1", RequestedAt: 42}); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if len(pub.matched) != 0 || len(pub.unmatched) != 1 {
		t.Fatalf("matched=%d unmatched=%d, want 0/1", len(pub.matched), len(pub.unmatched))
	}
	got := pub.unmatched[0]
	if got.RideID != "r1" || got.Reason != "no_drivers" || got.RequestedAt != 42 || got.FailedAt < before {
		t.Errorf("unexpected event: %+v", got)
	}
}

func TestHandlerUnmatchedOnMatchDeadline(t *testing.T) {
	pub := &fakePublisher{}
	h := newHandler(fakeMatcher{err: context.DeadlineExceeded, waitForDone: true}, pub, time.Millisecond)
	if err := h(context.Background(), events.RidePricedEvent{RideID: "r2"}); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if len(pub.unmatched) != 1 || pub.unmatched[0].Reason != "match_deadline" {
		t.Fatalf("unmatched = %+v, want one match_deadline", pub.unmatched)
	}
}

func TestHandlerReturnsUnmatchedPublishError(t *testing.T) {
	boom := errors.New("kafka down")
	pub := &fakePublisher{unmatchedErr: boom}
	h := newHandler(fakeMatcher{err: matching.ErrNoDriversAvailable}, pub, time.Second)
	if err := h(context.Background(), events.RidePricedEvent{RideID: "r3"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want publish error", err)
	}
}

func TestHandlerInfraErrorPublishesNothing(t *testing.T) {
	pub := &fakePublisher{}
	h := newHandler(fakeMatcher{err: errors.New("redis down")}, pub, time.Second)
	if err := h(context.Background(), events.RidePricedEvent{RideID: "r4"}); err == nil {
		t.Fatal("want error")
	}
	if len(pub.unmatched) != 0 || len(pub.matched) != 0 {
		t.Errorf("published on infra error: %+v %+v", pub.matched, pub.unmatched)
	}
}

func TestHandlerPublishesMatchedOnSuccess(t *testing.T) {
	pub := &fakePublisher{}
	h := newHandler(fakeMatcher{match: events.RideMatchedEvent{RideID: "r5", DriverID: "d"}}, pub, time.Second)
	if err := h(context.Background(), events.RidePricedEvent{RideID: "r5"}); err != nil {
		t.Fatal(err)
	}
	if len(pub.matched) != 1 || len(pub.unmatched) != 0 {
		t.Errorf("matched=%d unmatched=%d", len(pub.matched), len(pub.unmatched))
	}
}
