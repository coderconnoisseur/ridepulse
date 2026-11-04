package store

import (
	"sync"

	"ridepulse/services/assignment-service/internal/domain"
)

// Memory is a simple thread-safe store of ride outcomes keyed by ride ID.
//
// This is deliberately in-memory: outcomes live only for the run of a
// single benchmark and are read back via the HTTP API for verification.
// Persistence is not the point of this service; closing the pipeline so
// RPS benchmarks can measure end-to-end success is the point.
//
// A ride is either matched or unmatched. Matched wins: it overwrites an
// earlier unmatched outcome, and a later unmatched one is ignored.
type Memory struct {
	mu        sync.RWMutex
	matched   map[string]domain.Assignment
	unmatched map[string]domain.Outcome
}

func NewMemory() *Memory {
	return &Memory{
		matched:   make(map[string]domain.Assignment),
		unmatched: make(map[string]domain.Outcome),
	}
}

// PutMatched records a's ride as matched and reports whether it was new.
// A duplicate for an already-matched ride changes nothing and returns false.
func (m *Memory) PutMatched(a domain.Assignment) (isNew bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.matched[a.RideID]; ok {
		return false
	}
	m.matched[a.RideID] = a
	delete(m.unmatched, a.RideID)
	return true
}

// PutUnmatched records rideID as unmatched and reports whether it was new.
// It is ignored if the ride is already matched or already unmatched.
func (m *Memory) PutUnmatched(rideID, reason string, failedAt int64) (isNew bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.matched[rideID]; ok {
		return false
	}
	if _, ok := m.unmatched[rideID]; ok {
		return false
	}
	m.unmatched[rideID] = domain.Outcome{
		RideID:   rideID,
		Status:   domain.StatusUnmatched,
		Reason:   reason,
		FailedAt: failedAt,
	}
	return true
}

// Get returns the assignment only when the ride is matched.
func (m *Memory) Get(rideID string) (domain.Assignment, bool) {
	m.mu.RLock()
	a, ok := m.matched[rideID]
	m.mu.RUnlock()
	return a, ok
}

// Status returns the full outcome for a ride, matched or unmatched.
func (m *Memory) Status(rideID string) (domain.Outcome, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if a, ok := m.matched[rideID]; ok {
		return domain.Outcome{RideID: rideID, Status: domain.StatusMatched, Assignment: a}, true
	}
	o, ok := m.unmatched[rideID]
	return o, ok
}

// Count returns the number of matched rides (the benchmark reads this).
func (m *Memory) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.matched)
}
