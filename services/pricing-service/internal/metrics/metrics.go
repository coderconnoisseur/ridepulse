package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	EventsConsumed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pricing_events_consumed_total",
		Help: "Total RideRequested events consumed from Kafka.",
	})

	EventsPublished = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pricing_events_published_total",
		Help: "Total RidePriced events successfully published to Kafka.",
	})

	PublishErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pricing_publish_errors_total",
		Help: "RidePriced publish failures.",
	})

	ProcessingDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "pricing_processing_duration_seconds",
		Help:    "Time from picking a RideRequested off the pool to publishing the RidePriced event.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0},
	})
)

func Register() {
	prometheus.MustRegister(
		EventsConsumed,
		EventsPublished,
		PublishErrors,
		ProcessingDuration,
	)
}
