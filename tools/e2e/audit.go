package main

import (
	"fmt"
	"slices"
	"time"

	"ridepulse/pkg/events"
)

// topics is what actually happened, read back from Kafka.
type topics struct {
	requested []events.RideRequestedEvent
	priced    []events.RidePricedEvent
	matched   []events.RideMatchedEvent
	unmatched []events.RideUnmatchedEvent
}

// missingOutcomes returns requested rides with neither a matched nor an
// unmatched event.
func missingOutcomes(t topics) []string {
	done := make(map[string]bool, len(t.matched)+len(t.unmatched))
	for _, e := range t.matched {
		done[e.RideID] = true
	}
	for _, e := range t.unmatched {
		done[e.RideID] = true
	}
	var missing []string
	for _, e := range t.requested {
		if !done[e.RideID] {
			missing = append(missing, e.RideID)
		}
	}
	return missing
}

// audit checks the system's guarantees and returns every violation. accepted
// holds the ride IDs the gateway answered 201 for; minTripGap is the least time
// a driver can take between two rides.
func audit(t topics, accepted []string, minTripGap time.Duration) []string {
	var problems []string
	report := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	requested := make(map[string]bool, len(t.requested))
	for _, e := range t.requested {
		requested[e.RideID] = true
	}
	for _, id := range accepted {
		if !requested[id] {
			report("ride %s was accepted but never reached ride.requested", id)
		}
	}
	for _, id := range missingOutcomes(t) {
		report("ride %s has no outcome", id)
	}

	prices := make(map[string][]float64, len(t.priced))
	for _, e := range t.priced {
		prices[e.RideID] = append(prices[e.RideID], e.Price)
	}

	unmatched := make(map[string]bool, len(t.unmatched))
	for _, e := range t.unmatched {
		unmatched[e.RideID] = true
	}

	// Redeliveries repeat events, so each ride counts once from here on.
	first := make(map[string]events.RideMatchedEvent, len(t.matched))
	for _, e := range t.matched {
		if earlier, seen := first[e.RideID]; seen {
			if earlier.DriverID != e.DriverID {
				report("ride %s got two drivers: %s and %s", e.RideID, earlier.DriverID, e.DriverID)
			}
			continue
		}
		first[e.RideID] = e
		if unmatched[e.RideID] {
			report("ride %s was reported unmatched and then matched", e.RideID)
		}
		if !slices.Contains(prices[e.RideID], e.Price) || e.Currency != "INR" {
			report("ride %s: fare %v %s on ride.matched was never quoted by pricing", e.RideID, e.Price, e.Currency)
		}
	}

	claims := make(map[string][]int64)
	for _, e := range first {
		claims[e.DriverID] = append(claims[e.DriverID], e.MatchedAt)
	}
	for driver, times := range claims {
		slices.Sort(times)
		for i := 1; i < len(times); i++ {
			if gap := time.Duration(times[i]-times[i-1]) * time.Second; gap < minTripGap {
				report("driver %s started rides %s apart, inside one trip", driver, gap)
			}
		}
	}
	return problems
}
