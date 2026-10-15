package metrics

import (
	"runtime"

	"github.com/prometheus/client_golang/prometheus"
)


var (
	RedisGeoLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "redis_geo_query_latency_seconds",
			Help:    "Latency of Redis GEO queries",
			Buckets: prometheus.DefBuckets,
		},
	)

	DriverLockLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "driver_lock_latency_seconds",
			Help:    "Latency of driver lock attempts",
			Buckets: prometheus.DefBuckets,
		},
	)

	DriverLockSuccess = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "driver_lock_success_total",
			Help: "Total successful driver locks",
		},
	)

	DriverLockConflict = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "driver_lock_conflict_total",
			Help: "Total driver lock conflicts",
		},
	)
	DriverLockReleasedPhantom = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "driver_lock_released_phantom_total",
			Help: "Driver locks acquired by a batch goroutine but released because another goroutine had already been chosen as the batch winner. Non-zero values indicate the phantom-lock guard is doing work.",
		},
	)
	NearbyDriversGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "nearby_drivers_current",
			Help: "Current number of nearby drivers",
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

	MatchLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "matching_latency_seconds",
			Help:    "Ride matching latency",
			Buckets: prometheus.DefBuckets,
		},
	)

	NearbyDrivers = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "nearby_driver_count",
			Help:    "Number of nearby drivers",
			Buckets: []float64{1, 5, 10, 20, 50, 100},
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
		MatchLatency,
		NearbyDrivers,
		Goroutines,
		Retries,
		DLQMessages,
		RedisGeoLatency,
		NearbyDriversGauge,
		DriverLockLatency,
		DriverLockSuccess,
		DriverLockConflict,
		DriverLockReleasedPhantom,
	)
}
