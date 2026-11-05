package main

import (
	"strings"
	"testing"
	"time"

	"ridepulse/pkg/events"
)

func healthy() topics {
	return topics{
		requested: []events.RideRequestedEvent{{RideID: "a"}, {RideID: "b"}, {RideID: "c"}},
		priced: []events.RidePricedEvent{
			{RideID: "a", Price: 100}, {RideID: "a", Price: 150}, // quoted twice after a pricing crash
			{RideID: "b", Price: 90}, {RideID: "c", Price: 80},
		},
		matched: []events.RideMatchedEvent{
			{RideID: "a", DriverID: "d1", MatchedAt: 10, Price: 100, Currency: "INR"},
			{RideID: "a", DriverID: "d1", MatchedAt: 10, Price: 100, Currency: "INR"}, // redelivered
			{RideID: "b", DriverID: "d1", MatchedAt: 15, Price: 90, Currency: "INR"},
		},
		unmatched: []events.RideUnmatchedEvent{{RideID: "c"}},
	}
}

func TestAuditPassesHealthyRun(t *testing.T) {
	if got := audit(healthy(), []string{"a", "b", "c"}, 4*time.Second); len(got) != 0 {
		t.Fatalf("problems = %v", got)
	}
}

func TestAuditCatchesEachViolation(t *testing.T) {
	tests := []struct {
		name     string
		accepted []string
		breakIt  func(*topics)
		want     string
	}{
		{"accepted ride lost", []string{"lost"}, func(*topics) {}, "never reached"},
		{"no outcome", nil, func(x *topics) { x.unmatched = nil }, "ride c has no outcome"},
		{"two drivers", nil, func(x *topics) {
			x.matched = append(x.matched, events.RideMatchedEvent{RideID: "a", DriverID: "d2"})
		}, "two drivers"},
		{"unmatched then matched", nil, func(x *topics) {
			x.unmatched = append(x.unmatched, events.RideUnmatchedEvent{RideID: "a"})
		}, "unmatched and then matched"},
		{"unquoted fare", nil, func(x *topics) { x.matched[0].Price = 1 }, "never quoted"},
		{"missing currency", nil, func(x *topics) { x.matched[0].Currency = "" }, "never quoted"},
		{"double-booked driver", nil, func(x *topics) { x.matched[2].MatchedAt = 11 }, "inside one trip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := healthy()
			tt.breakIt(&run)
			got := strings.Join(audit(run, tt.accepted, 4*time.Second), "\n")
			if !strings.Contains(got, tt.want) {
				t.Fatalf("problems = %q, want one containing %q", got, tt.want)
			}
		})
	}
}
