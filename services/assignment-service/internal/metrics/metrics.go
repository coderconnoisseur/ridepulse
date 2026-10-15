package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	AssignmentsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "assignments_total",
		Help: "Total ride assignments observed end-to-end (ride.matched consumptions).",
	})

	// EndToEndLatency: time from api-gateway POST /rides receipt to
	// assignment-service consuming the matching ride.matched event.
	// Buckets tuned for a sub-second target with tail visibility up to 10s.
	EndToEndLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "end_to_end_latency_seconds",
		Help:    "Time from RideRequested emission to RideMatched consumption.",
		Buckets: []float64{0.05, 0.1, 0.2, 0.3, 0.5, 0.8, 1.2, 2.0, 3.0, 5.0, 10.0},
	})

	// MissingRequestedAt fires when a ride.matched event arrives without
	// its RequestedAt timestamp — indicates upstream schema drift (a
	// service in the chain dropped the field). Should always be zero.
	MissingRequestedAt = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "assignment_missing_requested_at_total",
		Help: "Matched events with no RequestedAt timestamp (indicates upstream schema drift).",
	})

	DLQMessages = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "assignment_dlq_messages_total",
		Help: "Messages routed to the dead-letter topic.",
	})
)

func Register() {
	prometheus.MustRegister(
		AssignmentsTotal,
		EndToEndLatency,
		MissingRequestedAt,
		DLQMessages,
	)
}
