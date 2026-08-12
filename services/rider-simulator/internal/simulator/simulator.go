package simulator

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
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
var errors int64
var totalLatency int64

func (s *Simulator) sendRide(ctx context.Context) {
	req := RideRequest{}
	req.Pickup.Lat = boxMinLat + rand.Float64()*boxSpan
	req.Pickup.Lng = boxMinLng + rand.Float64()*boxSpan
	req.Drop.Lat = boxMinLat + rand.Float64()*boxSpan
	req.Drop.Lng = boxMinLng + rand.Float64()*boxSpan
	body, _ := json.Marshal(req)
	start:= time.Now()
	resp,err:= s.client.Post(
		s.url,
		"application/json",
		bytes.NewBuffer(body),
	)
	latency := time.Since(start)
	atomic.AddInt64(&requests, 1)
	atomic.AddInt64(&totalLatency, latency.Microseconds())
	if err != nil || (resp != nil && resp.StatusCode >= 400) {
		atomic.AddInt64(&errors, 1)
	}
	if resp != nil {
		resp.Body.Close()
	}
}
func (s *Simulator) Run(ctx context.Context, rps int, maxConcurrency int) {
	interval := time.Second / time.Duration(rps)
	// semaphore to avoid goroutine explosion
	sem := make(chan struct{}, maxConcurrency)
	// metrics
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		for range ticker.C {
			r := atomic.SwapInt64(&requests, 0)
			e := atomic.SwapInt64(&errors, 0)
			l := atomic.SwapInt64(&totalLatency, 0)
			var avg int64
			if r > 0 {
				avg = l / r
			}
			log.Printf("RPS: %d | Errors: %d | Avg Latency(us): %d", r, e, avg)
		}
	}()
	ticker := time.NewTicker(interval)
	for {
		select {
		case <-ticker.C:
			sem <- struct{}{} // acquire slot
			go func() {
				defer func() { <-sem }() // release
				s.sendRide(ctx)
			}()
		case <-ctx.Done():
			return
		}
	}
}
