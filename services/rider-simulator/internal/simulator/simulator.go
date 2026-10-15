package simulator

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"sync/atomic"
	"time"
)

// Bounding box for pickup and drop sampling.
//
// The driver-simulator seeds drivers in [12.9, 12.92] × [77.6, 77.62] and lets
// them drift by ±0.001 per tick. Rides must be sampled from a region that
// overlaps this driver cluster, otherwise Redis GEORADIUS returns zero
// candidates and every ride fails with "no drivers available."
//
// This box is centred on the driver cluster and extends ~3 km in each
// direction, so pickups and drops spread across ~6 km × 6 km — enough to
// break the fixed-point hotspot that made every ride land on the same top-5
// nearest drivers.
const (
	boxMinLat = 12.88
	boxMinLng = 77.58
	boxSpan   = 0.06 // ~6 km at this latitude
)

type Simulator struct {
	client *http.Client
	url    string
}

func New(url string) *Simulator {
	return &Simulator{
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
		url: url,
	}
}

type RideRequest struct {
	Pickup struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	} `json:"pickup"`

	Drop struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	} `json:"drop"`
}

var requests int64
var errCount int64
var totalLatency int64
var dropped int64

func (s *Simulator) sendRide(ctx context.Context) {
	req := RideRequest{}
	req.Pickup.Lat = boxMinLat + rand.Float64()*boxSpan
	req.Pickup.Lng = boxMinLng + rand.Float64()*boxSpan
	req.Drop.Lat = boxMinLat + rand.Float64()*boxSpan
	req.Drop.Lng = boxMinLng + rand.Float64()*boxSpan
	body, _ := json.Marshal(req)
	start := time.Now()
	resp, err := s.client.Post(
		s.url,
		"application/json",
		bytes.NewBuffer(body),
	)
	latency := time.Since(start)
	atomic.AddInt64(&requests, 1)
	atomic.AddInt64(&totalLatency, latency.Microseconds())
	if err != nil || (resp != nil && resp.StatusCode >= 400) {
		atomic.AddInt64(&errCount, 1)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

// tickInterval is well above the ~15 ms Windows timer resolution, so the
// ticker fires reliably; each tick sends a batch sized by the Pacer.
const tickInterval = 20 * time.Millisecond

// Pacer converts elapsed wall time into a whole number of sends at a target
// rate, carrying the fractional remainder to the next tick so the long-run
// rate is exact and a late tick catches up.
type Pacer struct {
	rps float64
	acc float64
}

func NewPacer(rps int) *Pacer {
	return &Pacer{rps: float64(rps)}
}

// Next returns how many requests are due after elapsed time has passed.
func (p *Pacer) Next(elapsed time.Duration) int {
	p.acc += p.rps * elapsed.Seconds()
	n := int(math.Floor(p.acc))
	p.acc -= float64(n)
	return n
}

// Run sends rps requests per second until ctx is cancelled. If maxConcurrency
// requests are already in flight, the send is skipped and counted as dropped
// rather than blocking the ticker.
func (s *Simulator) Run(ctx context.Context, rps int, maxConcurrency int) {
	// semaphore to avoid goroutine explosion
	sem := make(chan struct{}, maxConcurrency)
	// metrics
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
			r := atomic.SwapInt64(&requests, 0)
			e := atomic.SwapInt64(&errCount, 0)
			l := atomic.SwapInt64(&totalLatency, 0)
			d := atomic.SwapInt64(&dropped, 0)
			var avg int64
			if r > 0 {
				avg = l / r
			}
			log.Printf("RPS: %d | Errors: %d | Dropped: %d | Avg Latency(us): %d", r, e, d, avg)
		}
	}()
	pacer := NewPacer(rps)
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	last := time.Now()
	for {
		select {
		case now := <-ticker.C:
			n := pacer.Next(now.Sub(last))
			last = now
			for i := 0; i < n; i++ {
				select {
				case sem <- struct{}{}: // acquire slot
					go func() {
						defer func() { <-sem }() // release
						s.sendRide(ctx)
					}()
				default:
					atomic.AddInt64(&dropped, 1)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}
