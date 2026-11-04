package dashboard

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ridepulse/pkg/events"
)

const (
	// A frame carries at most this many map events, so a burst can't flood the
	// browser. The totals still count everything.
	maxFrameEvents = 60
	recentRides    = 8
	maxNotices     = 6

	pendingTTL    = time.Minute
	latencyWindow = 10 * time.Second
)

// MapMatch is one matched ride as the map and the recent-rides list show it.
type MapMatch struct {
	RideID    string     `json:"ride_id"`
	DriverID  string     `json:"driver_id"`
	Pickup    [2]float64 `json:"pickup"`
	Price     float64    `json:"price"`
	Surge     float64    `json:"surge"`
	LatencyMs int64      `json:"latency_ms"`
}

// Rates are per-second rates at each pipeline stage.
type Rates struct {
	Requested float64 `json:"requested"`
	Priced    float64 `json:"priced"`
	Decided   float64 `json:"decided"`
	Matched   float64 `json:"matched"`
	Unmatched float64 `json:"unmatched"`
}

// Totals count since the dashboard started.
type Totals struct {
	Requested  int64 `json:"requested"`
	Matched    int64 `json:"matched"`
	Unmatched  int64 `json:"unmatched"`
	DeadLetter int64 `json:"dead_letter"`
	Duplicates int64 `json:"duplicates"`
	Replays    int64 `json:"replays"`
	Retries    int64 `json:"retries"`
}

// Frame is one second of dashboard state. The live page and the recorded
// replay render the same frames.
type Frame struct {
	At        int64            `json:"at"`
	Demand    int              `json:"demand"`
	Rush      bool             `json:"rush"`
	Surge     float64          `json:"surge"`
	Free      [][3]float64     `json:"free"`
	Busy      [][3]float64     `json:"busy"`
	Rates     Rates            `json:"rates"`
	Queues    map[string]int64 `json:"queues"`
	Services  map[string]bool  `json:"services"`
	Totals    Totals           `json:"totals"`
	P95Ms     int64            `json:"p95_ms"`
	Matches   []MapMatch       `json:"matches"`
	Unmatched [][2]float64     `json:"unmatched"`
	Recent    []MapMatch       `json:"recent"`
	Notices   []string         `json:"notices"`
}

type pendingRide struct {
	pickup [2]float64
	at     time.Time
}

type latencySample struct {
	at time.Time
	ms int64
}

// State collects what happened since the last frame. Safe for concurrent use.
type State struct {
	mu        sync.Mutex
	pending   map[string]pendingRide
	matches   []MapMatch
	unmatched [][2]float64
	recent    []MapMatch
	notices   []string
	latencies []latencySample
	positions map[int][2]float64
	last      *lastRide
}

// lastRide is enough to redeliver a recent ride for the duplicate demo.
type lastRide struct {
	event  events.RideMatchedEvent
	pickup [2]float64
}

// NewState returns an empty State.
func NewState() *State {
	return &State{pending: make(map[string]pendingRide), positions: make(map[int][2]float64)}
}

// AddRide remembers a requested ride's pickup, which later events don't carry.
func (s *State) AddRide(rideID string, pickup [2]float64, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[rideID] = pendingRide{pickup, now}
}

func (s *State) takePending(rideID string) ([2]float64, bool) {
	p, ok := s.pending[rideID]
	if ok {
		delete(s.pending, rideID)
	}
	return p.pickup, ok
}

// OnMatched records a ride.matched event. A redelivered event has no pending
// ride any more, so it stays off the map and out of the latency numbers.
func (s *State) OnMatched(e events.RideMatchedEvent, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pickup, ok := s.takePending(e.RideID)
	if !ok {
		return
	}
	latency := (now.UnixNano() - e.RequestedAt) / int64(time.Millisecond)
	m := MapMatch{
		RideID: e.RideID, DriverID: e.DriverID, Pickup: pickup,
		Price: e.Price, Surge: e.SurgeMultiplier, LatencyMs: latency,
	}
	if len(s.matches) < maxFrameEvents {
		s.matches = append(s.matches, m)
	}
	s.recent = append([]MapMatch{m}, s.recent...)
	if len(s.recent) > recentRides {
		s.recent = s.recent[:recentRides]
	}
	s.latencies = append(s.latencies, latencySample{now, latency})
	s.last = &lastRide{e, pickup}
}

// OnUnmatched records a ride.unmatched event.
func (s *State) OnUnmatched(e events.RideUnmatchedEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pickup, ok := s.takePending(e.RideID)
	if ok && len(s.unmatched) < maxFrameEvents {
		s.unmatched = append(s.unmatched, pickup)
	}
}

// Notice adds a line to the on-screen event log.
func (s *State) Notice(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notices = append([]string{text}, s.notices...)
	if len(s.notices) > maxNotices {
		s.notices = s.notices[:maxNotices]
	}
}

// LastRide returns the most recent matched ride, if any.
func (s *State) LastRide() (events.RideMatchedEvent, [2]float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		return events.RideMatchedEvent{}, [2]float64{}, false
	}
	return s.last.event, s.last.pickup, true
}

// FillFrame moves the events gathered since the last frame into f, and splits
// drivers into free and busy. free holds the drivers now in Redis search. A
// driver seen earlier but missing from it is on a trip, at its last position.
func (s *State) FillFrame(f *Frame, free map[string][2]float64, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f.Matches, s.matches = s.matches, nil
	f.Unmatched, s.unmatched = s.unmatched, nil
	f.Recent = append([]MapMatch(nil), s.recent...)
	f.Notices = append([]string(nil), s.notices...)

	cutoff := now.Add(-latencyWindow)
	s.latencies = slices.DeleteFunc(s.latencies, func(l latencySample) bool { return l.at.Before(cutoff) })
	f.P95Ms = p95(s.latencies)

	for id, p := range s.pending {
		if now.Sub(p.at) > pendingTTL {
			delete(s.pending, id)
		}
	}

	f.Free = make([][3]float64, 0, len(free))
	inSearch := make(map[int]bool, len(free))
	for name, pos := range free {
		id, ok := driverNumber(name)
		if !ok {
			continue
		}
		inSearch[id] = true
		s.positions[id] = pos
		f.Free = append(f.Free, driverPoint(id, pos))
	}
	f.Busy = make([][3]float64, 0)
	for id, pos := range s.positions {
		if !inSearch[id] {
			f.Busy = append(f.Busy, driverPoint(id, pos))
		}
	}
}

func driverNumber(name string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(name, "driver-"))
	return n, err == nil
}

func driverPoint(id int, pos [2]float64) [3]float64 {
	return [3]float64{float64(id), round5(pos[0]), round5(pos[1])}
}

func round5(v float64) float64 { return math.Round(v*1e5) / 1e5 }

func p95(samples []latencySample) int64 {
	if len(samples) == 0 {
		return 0
	}
	ms := make([]int64, len(samples))
	for i, s := range samples {
		ms[i] = s.ms
	}
	slices.Sort(ms)
	return ms[(len(ms)*95-1)/100]
}
