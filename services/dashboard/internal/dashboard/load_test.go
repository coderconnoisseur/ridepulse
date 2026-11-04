package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRidesDueIsExactOverTime(t *testing.T) {
	var carry float64
	total := 0
	for range 50 { // 50 ticks of 20 ms is 1 s
		var n int
		n, carry = ridesDue(33, 20*time.Millisecond, carry)
		total += n
	}
	if total < 32 || total > 33 {
		t.Fatalf("sent %d in 1 s, want about 33", total)
	}
}

func TestDemandIsClamped(t *testing.T) {
	l := NewLoad("", NewState(), 10)
	l.SetDemand(MaxDemand + 1)
	if l.Demand() != MaxDemand {
		t.Fatalf("demand = %d", l.Demand())
	}
	l.SetDemand(-5)
	if l.Demand() != 0 {
		t.Fatalf("demand = %d", l.Demand())
	}
}

func TestRushRaisesDemandThenEnds(t *testing.T) {
	l := NewLoad("", NewState(), 10)
	now := time.Unix(1000, 0)
	l.StartRush(now)
	if got := l.Current(now.Add(time.Second)); got != RushDemand {
		t.Fatalf("during rush = %d, want %d", got, RushDemand)
	}
	if got := l.Current(now.Add(rushDuration + time.Second)); got != 10 {
		t.Fatalf("after rush = %d, want 10", got)
	}
}

func TestLoadRemembersPickupsFromGateway(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"ride_id":"r1","status":"REQUESTED"}`))
	}))
	defer srv.Close()

	state := NewState()
	l := NewLoad(srv.URL, state, 100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state.mu.Lock()
		_, ok := state.pending["r1"]
		state.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no pickup remembered after %d requests", hits.Load())
}
