package simulator

import (
	"testing"
	"time"
)

func TestPacerYieldsExactRPS(t *testing.T) {
	// Tick gaps sum to exactly 1 s; the late patterns include one very late tick.
	steady := func() []time.Duration {
		d := make([]time.Duration, 50)
		for i := range d {
			d[i] = 20 * time.Millisecond
		}
		return d
	}()
	late := []time.Duration{
		20 * time.Millisecond, 35 * time.Millisecond, 5 * time.Millisecond,
		140 * time.Millisecond, 20 * time.Millisecond, 250 * time.Millisecond,
		30 * time.Millisecond, 500 * time.Millisecond,
	}
	for name, gaps := range map[string][]time.Duration{"steady": steady, "late": late} {
		var total time.Duration
		for _, g := range gaps {
			total += g
		}
		if total != time.Second {
			t.Fatalf("%s: gaps sum to %v, want 1s", name, total)
		}
		for _, rps := range []int{50, 1000, 3000} {
			p := NewPacer(rps)
			sent := 0
			for _, g := range gaps {
				sent += p.Next(g)
			}
			if sent < rps-1 || sent > rps+1 {
				t.Errorf("%s rps=%d: sent %d, want %d±1", name, rps, sent, rps)
			}
		}
	}
}

func TestPacerCarriesFraction(t *testing.T) {
	p := NewPacer(50) // 1 request per 20 ms, but 10 ms ticks yield 0.5 each
	if n := p.Next(10 * time.Millisecond); n != 0 {
		t.Fatalf("first tick = %d, want 0", n)
	}
	if n := p.Next(10 * time.Millisecond); n != 1 {
		t.Fatalf("second tick = %d, want 1", n)
	}
}
