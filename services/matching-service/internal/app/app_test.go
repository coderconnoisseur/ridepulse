package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"ridepulse/pkg/events"
	"ridepulse/services/matching-service/internal/matching"
)

type fakeMatcher struct {
	match events.RideMatchedEvent
	err   error
}

func (f fakeMatcher) Match(context.Context, events.RidePricedEvent) (events.RideMatchedEvent, error) {
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
	h := newHandler(fakeMatcher{err: matching.ErrNoDriversAvailable}, pub)
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

func TestHandlerReturnsUnmatchedPublishError(t *testing.T) {
	boom := errors.New("kafka down")
	pub := &fakePublisher{unmatchedErr: boom}
	h := newHandler(fakeMatcher{err: matching.ErrNoDriversAvailable}, pub)
	if err := h(context.Background(), events.RidePricedEvent{RideID: "r3"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want publish error", err)
	}
}

func TestHandlerInfraErrorPublishesNothing(t *testing.T) {
	pub := &fakePublisher{}
	h := newHandler(fakeMatcher{err: errors.New("redis down")}, pub)
	if err := h(context.Background(), events.RidePricedEvent{RideID: "r4"}); err == nil {
		t.Fatal("want error")
	}
	if len(pub.unmatched) != 0 || len(pub.matched) != 0 {
		t.Errorf("published on infra error: %+v %+v", pub.matched, pub.unmatched)
	}
}

func TestHandlerPublishesMatchedOnSuccess(t *testing.T) {
	pub := &fakePublisher{}
	h := newHandler(fakeMatcher{match: events.RideMatchedEvent{RideID: "r5", DriverID: "d"}}, pub)
	if err := h(context.Background(), events.RidePricedEvent{RideID: "r5"}); err != nil {
		t.Fatal(err)
	}
	if len(pub.matched) != 1 || len(pub.unmatched) != 0 {
		t.Errorf("matched=%d unmatched=%d", len(pub.matched), len(pub.unmatched))
	}
}
