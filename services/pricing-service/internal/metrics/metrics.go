package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	EventsConsumed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pricing_events_consumed_total",
		Help: "RideRequested handler attempts; pricing_retries_total counts the retries among them.",
	})

	EventsPublished = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pricing_events_published_total",
		Help: "Total RidePriced events successfully published to Kafka.",
	})

	PublishErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pricing_publish_errors_total",
		Help: "RidePriced publish failures.",
	})

	Retries = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pricing_retries_total",
		Help: "Handler attempts that failed and were retried.",
	})

	DLQMessages = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "pricing_dlq_messages_total",
		Help: "Messages routed to the dead-letter topic.",
	})

	ProcessingDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "pricing_processing_duration_seconds",
		Help:    "Time to price and publish one RideRequested handler attempt (observed per attempt, retries included).",
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0},
	})
)

func Register() {
	prometheus.MustRegister(
		EventsConsumed,
		EventsPublished,
		PublishErrors,
		Retries,
		DLQMessages,
		ProcessingDuration,
	)
}
