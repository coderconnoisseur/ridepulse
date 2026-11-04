package dashboard

import (
	"fmt"
	"testing"
	"time"

	"ridepulse/pkg/events"
)

var testNow = time.Unix(1000, 0)

func TestOnMatchedAddsMapEventAndLatency(t *testing.T) {
	s := NewState()
	s.AddRide("r1", [2]float64{12.9, 77.6}, testNow)
	s.OnMatched(events.RideMatchedEvent{
		RideID: "r1", DriverID: "driver-7", Price: 134.18, SurgeMultiplier: 1.5,
		RequestedAt: testNow.Add(-200 * time.Millisecond).UnixNano(),
	}, testNow)

	var f Frame
	s.FillFrame(&f, nil, testNow)
	if len(f.Matches) != 1 || f.Matches[0].DriverID != "driver-7" || f.Matches[0].Pickup != [2]float64{12.9, 77.6} {
		t.Fatalf("matches = %+v", f.Matches)
	}
	if f.Recent[0].Price != 134.18 || f.Recent[0].Surge != 1.5 {
		t.Fatalf("recent = %+v", f.Recent)
	}
	if f.P95Ms != 200 {
		t.Fatalf("p95 = %d, want 200", f.P95Ms)
	}

	var next Frame
	s.FillFrame(&next, nil, testNow)
	if len(next.Matches) != 0 {
		t.Fatal("map events should appear in one frame only")
	}
	if len(next.Recent) != 1 {
		t.Fatal("recent rides should persist between frames")
	}
}

func TestRedeliveredMatchDoesNotCountTwice(t *testing.T) {
	s := NewState()
	s.AddRide("r1", [2]float64{1, 2}, testNow)
	e := events.RideMatchedEvent{RideID: "r1", DriverID: "d", RequestedAt: testNow.UnixNano()}
	s.OnMatched(e, testNow)
	s.OnMatched(e, testNow)

	var f Frame
	s.FillFrame(&f, nil, testNow)
	if len(f.Matches) != 1 || len(f.Recent) != 1 {
		t.Fatalf("matches=%d recent=%d, want 1 and 1", len(f.Matches), len(f.Recent))
	}
}

func TestOnUnmatchedAddsPickup(t *testing.T) {
	s := NewState()
	s.AddRide("r1", [2]float64{1, 2}, testNow)
	s.OnUnmatched(events.RideUnmatchedEvent{RideID: "r1"})
	s.OnUnmatched(events.RideUnmatchedEvent{RideID: "unknown"})

	var f Frame
	s.FillFrame(&f, nil, testNow)
	if len(f.Unmatched) != 1 || f.Unmatched[0] != [2]float64{1, 2} {
		t.Fatalf("unmatched = %v", f.Unmatched)
	}
}

func TestFrameEventsAreCapped(t *testing.T) {
	s := NewState()
	for i := range maxFrameEvents + 10 {
		id := fmt.Sprintf("r%d", i)
		s.AddRide(id, [2]float64{}, testNow)
		s.OnUnmatched(events.RideUnmatchedEvent{RideID: id})
	}
	var f Frame
	s.FillFrame(&f, nil, testNow)
	if len(f.Unmatched) != maxFrameEvents {
		t.Fatalf("unmatched = %d, want capped at %d", len(f.Unmatched), maxFrameEvents)
	}
}

func TestFreeAndBusySplit(t *testing.T) {
	s := NewState()
	var f Frame
	s.FillFrame(&f, map[string][2]float64{"driver-1": {12.9, 77.6}, "driver-2": {12.91, 77.61}}, testNow)
	if len(f.Free) != 2 || len(f.Busy) != 0 {
		t.Fatalf("free=%d busy=%d", len(f.Free), len(f.Busy))
	}

	// driver-2 left search: it's on a trip, shown where it was last seen.
	var g Frame
	s.FillFrame(&g, map[string][2]float64{"driver-1": {12.9, 77.6}}, testNow)
	if len(g.Free) != 1 || len(g.Busy) != 1 || g.Busy[0] != [3]float64{2, 12.91, 77.61} {
		t.Fatalf("free=%v busy=%v", g.Free, g.Busy)
	}

	// Back in search: free again.
	var h Frame
	s.FillFrame(&h, map[string][2]float64{"driver-1": {12.9, 77.6}, "driver-2": {12.92, 77.62}}, testNow)
	if len(h.Free) != 2 || len(h.Busy) != 0 {
		t.Fatalf("free=%d busy=%d", len(h.Free), len(h.Busy))
	}
}

func TestOldPendingRidesAreForgotten(t *testing.T) {
	s := NewState()
	s.AddRide("old", [2]float64{}, testNow)
	var f Frame
	s.FillFrame(&f, nil, testNow.Add(pendingTTL+time.Second))
	if len(s.pending) != 0 {
		t.Fatal("old pending ride kept")
	}
}

func TestP95(t *testing.T) {
	var samples []latencySample
	for i := 1; i <= 100; i++ {
		samples = append(samples, latencySample{ms: int64(i)})
	}
	if got := p95(samples); got != 95 {
		t.Fatalf("p95 = %d, want 95", got)
	}
	if got := p95(nil); got != 0 {
		t.Fatalf("p95(nil) = %d, want 0", got)
	}
	if got := p95(samples[:1]); got != 1 {
		t.Fatalf("p95 of one sample = %d, want 1", got)
	}
}

func TestLastRide(t *testing.T) {
	s := NewState()
	if _, _, ok := s.LastRide(); ok {
		t.Fatal("no ride yet")
	}
	s.AddRide("r1", [2]float64{1, 2}, testNow)
	s.OnMatched(events.RideMatchedEvent{RideID: "r1", DriverID: "d"}, testNow)
	e, pickup, ok := s.LastRide()
	if !ok || e.RideID != "r1" || pickup != [2]float64{1, 2} {
		t.Fatalf("LastRide = %+v %v %v", e, pickup, ok)
	}
}
