package store

import (
	"sync"

	"ridepulse/services/assignment-service/internal/domain"
)

// Memory is a simple thread-safe map keyed by ride ID.
//
// This is deliberately in-memory: assignments live only for the run of a
// single benchmark and are read back via the /assignments/<rideID>
// endpoint for verification. Persistence is not the point of this
// service — closing the pipeline so RPS benchmarks can measure end-to-end
// success is the point.
type Memory struct {
	mu       sync.RWMutex
	byRideID map[string]domain.Assignment
}

func NewMemory() *Memory {
	return &Memory{byRideID: make(map[string]domain.Assignment)}
}

func (m *Memory) Put(a domain.Assignment) {
	m.mu.Lock()
	m.byRideID[a.RideID] = a
	m.mu.Unlock()
}

func (m *Memory) Get(rideID string) (domain.Assignment, bool) {
	m.mu.RLock()
	a, ok := m.byRideID[rideID]
	m.mu.RUnlock()
	return a, ok
}

func (m *Memory) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.byRideID)
}
