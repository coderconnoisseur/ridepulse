package app

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"ridepulse/services/assignment-service/internal/domain"
	"ridepulse/services/assignment-service/internal/metrics"
	"ridepulse/services/assignment-service/internal/store"
)

// counterDelta runs fn and returns how far c moved. Reading before and after
// keeps the test independent of other tests using the package-level metrics.
func counterDelta(c prometheus.Collector, fn func()) float64 {
	before := testutil.ToFloat64(c)
	fn()
	return testutil.ToFloat64(c) - before
}

func TestHandleMatchedCountsEachRideOnce(t *testing.T) {
	h := handleMatched(store.NewMemory())
	event := domain.RideMatchedEvent{RideID: "ride-metric-m", DriverID: "d1", RequestedAt: 1}

	var dups float64
	total := counterDelta(metrics.AssignmentsTotal, func() {
		dups = counterDelta(metrics.AssignmentDuplicates, func() {
			for i := 0; i < 2; i++ {
				if err := h(context.Background(), event); err != nil {
					t.Fatalf("handleMatched: %v", err)
				}
			}
		})
	})
	if total != 1 {
		t.Errorf("assignments_total moved by %v, want 1", total)
	}
	if dups != 1 {
		t.Errorf("assignment_duplicates_total moved by %v, want 1", dups)
	}
}

func TestHandleUnmatchedCountsEachRideOnce(t *testing.T) {
	h := handleUnmatched(store.NewMemory())
	event := domain.RideUnmatchedEvent{RideID: "ride-metric-u", Reason: "no_drivers", RequestedAt: 1, FailedAt: 2}

	var dups float64
	total := counterDelta(metrics.UnmatchedTotal.WithLabelValues("no_drivers"), func() {
		dups = counterDelta(metrics.UnmatchedDuplicates, func() {
			for i := 0; i < 2; i++ {
				if err := h(context.Background(), event); err != nil {
					t.Fatalf("handleUnmatched: %v", err)
				}
			}
		})
	})
	if total != 1 {
		t.Errorf("unmatched_total moved by %v, want 1", total)
	}
	if dups != 1 {
		t.Errorf("unmatched_duplicates_total moved by %v, want 1", dups)
	}
}
