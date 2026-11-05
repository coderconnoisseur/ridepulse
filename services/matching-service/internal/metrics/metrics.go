package metrics

import (
	"runtime"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	DriverClaimLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "driver_claim_latency_seconds",
			Help:    "Latency of the atomic claim of the nearest free driver, as seen by the client",
			Buckets: prometheus.DefBuckets,
		},
	)
)

var (
	MatchRequests = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "matching_requests_total",
			Help: "Total ride matching requests",
		},
	)

	MatchSuccess = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "matching_success_total",
			Help: "Total successful matches",
		},
	)

	MatchFailures = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "matching_failures_total",
			Help: "Total failed matches",
		},
	)

	MatchIdempotentReplays = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "match_idempotent_replays_total",
			Help: "Match calls for rides that already had an assigned driver (retries/redeliveries); the existing driver was returned.",
		},
	)

	MatchUnmatchedPublished = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "match_unmatched_published_total",
			Help: "ride.unmatched events published, by reason.",
		},
		[]string{"reason"},
	)

	MatchLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "matching_latency_seconds",
			Help:    "Ride matching latency",
			Buckets: prometheus.DefBuckets,
		},
	)

	Retries = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "matching_retries_total",
			Help: "Handler attempts that failed and were retried.",
		},
	)

	DLQMessages = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "matching_dlq_messages_total",
			Help: "Messages routed to the dead-letter topic.",
		},
	)

	Goroutines = prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "goroutine_count",
			Help: "Current number of goroutines",
		},
		func() float64 {
			return float64(runtime.NumGoroutine())
		},
	)
)

func Register() {
	prometheus.MustRegister(
		MatchRequests,
		MatchSuccess,
		MatchFailures,
		MatchIdempotentReplays,
		MatchUnmatchedPublished,
		MatchLatency,
		Goroutines,
		Retries,
		DLQMessages,
		DriverClaimLatency,
	)
}
