package pricing

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSurgeMultiplier(t *testing.T) {
	tests := []struct {
		name        string
		recentRides int64
		freeDrivers int64
		want        float64
	}{
		{"no demand, no drivers", 0, 0, 1.0},
		{"no demand", 0, 1000, 1.0},
		{"demand below supply", 250, 700, 1.0},
		{"demand equals supply", 400, 400, 1.0},
		{"demand above supply", 500, 400, 1.3},
		{"demand double supply", 800, 400, 2.0},
		{"capped", 5000, 100, 3.0},
		{"no free drivers", 10, 0, 3.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SurgeMultiplier(tt.recentRides, tt.freeDrivers); got != tt.want {
				t.Fatalf("SurgeMultiplier(%d, %d) = %v, want %v", tt.recentRides, tt.freeDrivers, got, tt.want)
			}
		})
	}
}

func TestSurgeTrackerStartsAtOne(t *testing.T) {
	if got := NewSurgeTracker().Current(); got != 1.0 {
		t.Fatalf("Current = %v, want 1.0", got)
	}
}

func TestSurgeTrackerCountsOnlyTheWindow(t *testing.T) {
	s := NewSurgeTracker()
	for range 800 {
		s.RecordRide()
	}
	s.Tick(400)
	if got := s.Current(); got != 2.0 {
		t.Fatalf("after burst Current = %v, want 2.0", got)
	}

	// The burst stays in the window, then ages out once it has fully passed.
	for range surgeWindow - 1 {
		s.Tick(400)
		if got := s.Current(); got != 2.0 {
			t.Fatalf("inside window Current = %v, want 2.0", got)
		}
	}
	s.Tick(400)
	if got := s.Current(); got != 1.0 {
		t.Fatalf("after window Current = %v, want 1.0", got)
	}
}

func TestRunSurgeUpdatesFromDriverCount(t *testing.T) {
	s := NewSurgeTracker()
	for range 100 {
		s.RecordRide()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunSurge(ctx, s, func(context.Context) (int64, error) { return 50, nil }, time.Millisecond)
	}()

	deadline := time.After(2 * time.Second)
	for s.Current() != 2.0 {
		select {
		case <-deadline:
			t.Fatalf("Current = %v, want 2.0", s.Current())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestRunSurgeFallsBackToOneWhenDriverCountFails(t *testing.T) {
	s := NewSurgeTracker()
	for range 800 {
		s.RecordRide()
	}
	s.Tick(100)
	if s.Current() != 3.0 {
		t.Fatalf("setup: Current = %v, want 3.0", s.Current())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunSurge(ctx, s, func(context.Context) (int64, error) { return 0, errors.New("redis down") }, time.Millisecond)
	}()

	deadline := time.After(2 * time.Second)
	for s.Current() != 1.0 {
		select {
		case <-deadline:
			t.Fatalf("Current = %v, want fallback to 1.0", s.Current())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
}
