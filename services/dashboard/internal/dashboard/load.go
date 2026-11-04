package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"sync/atomic"
	"time"
)

const (
	// Rides start around the simulated drivers in Bengaluru.
	boxMinLat = 12.88
	boxMinLng = 77.58
	boxSpan   = 0.06

	loadTick       = 20 * time.Millisecond
	maxInFlight    = 300
	maxIdlePerHost = maxInFlight

	// RushDemand is the demand during a rush hour, far above what the demo's
	// drivers can serve.
	RushDemand   = 300
	rushDuration = 25 * time.Second
	// MaxDemand caps the slider.
	MaxDemand = 400
)

// Load is the demo's rider simulator. Unlike rider-simulator it can change rate
// while running, and it remembers pickups so rides can be drawn on the map.
type Load struct {
	url    string
	state  *State
	client *http.Client

	demand    atomic.Int64
	rushUntil atomic.Int64 // UnixNano
}

// NewLoad returns a generator posting to the gateway's rides URL.
func NewLoad(url string, state *State, demand int) *Load {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = maxIdlePerHost
	transport.MaxIdleConnsPerHost = maxIdlePerHost
	l := &Load{url: url, state: state, client: &http.Client{Timeout: 5 * time.Second, Transport: transport}}
	l.SetDemand(demand)
	return l
}

// SetDemand sets the steady requests per second, clamped to [0, MaxDemand].
func (l *Load) SetDemand(rps int) {
	l.demand.Store(int64(max(0, min(rps, MaxDemand))))
}

// Demand returns the steady requests per second.
func (l *Load) Demand() int { return int(l.demand.Load()) }

// StartRush raises demand to RushDemand for a short time.
func (l *Load) StartRush(now time.Time) { l.rushUntil.Store(now.Add(rushDuration).UnixNano()) }

// InRush reports whether a rush hour is running.
func (l *Load) InRush(now time.Time) bool { return now.UnixNano() < l.rushUntil.Load() }

// Current returns the requests per second to send now.
func (l *Load) Current(now time.Time) int {
	if l.InRush(now) {
		return RushDemand
	}
	return l.Demand()
}

// Run sends rides until ctx is cancelled. If too many are in flight, a send is
// skipped rather than blocking the ticker.
func (l *Load) Run(ctx context.Context) {
	sem := make(chan struct{}, maxInFlight)
	ticker := time.NewTicker(loadTick)
	defer ticker.Stop()
	last := time.Now()
	var carry float64
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			var n int
			n, carry = ridesDue(l.Current(now), now.Sub(last), carry)
			last = now
			for range n {
				select {
				case sem <- struct{}{}:
					go func() {
						defer func() { <-sem }()
						l.send(ctx)
					}()
				default:
				}
			}
		}
	}
}

// ridesDue converts elapsed time into whole rides at rps, carrying the
// remainder so the long-run rate is exact.
func ridesDue(rps int, elapsed time.Duration, carry float64) (int, float64) {
	carry += float64(rps) * elapsed.Seconds()
	n := math.Floor(carry)
	return int(n), carry - n
}

type point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

func randomPoint() point {
	return point{boxMinLat + rand.Float64()*boxSpan, boxMinLng + rand.Float64()*boxSpan}
}

func (l *Load) send(ctx context.Context) {
	pickup := randomPoint()
	body, err := json.Marshal(struct {
		Pickup point `json:"pickup"`
		Drop   point `json:"drop"`
	}{pickup, randomPoint()})
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.client.Do(req)
	if err != nil {
		return // the gateway's own metrics show the failure
	}
	defer resp.Body.Close()
	var created struct {
		RideID string `json:"ride_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err == nil && created.RideID != "" {
		l.state.AddRide(created.RideID, [2]float64{pickup.Lat, pickup.Lng}, time.Now())
	}
	io.Copy(io.Discard, resp.Body) // a connection is only reused once fully read
}
