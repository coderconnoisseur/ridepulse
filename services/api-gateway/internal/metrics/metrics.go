package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	RequestsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gateway_requests_total",
		Help: "Total /rides requests accepted by api-gateway.",
	})

	RequestDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "gateway_request_duration_seconds",
		Help:    "Latency of POST /rides handler (includes Kafka publish RTT).",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.5, 1.0, 2.0},
	})

	PublishErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gateway_publish_errors_total",
		Help: "Kafka publish failures on RideRequested.",
	})

	BadRequests = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gateway_bad_requests_total",
		Help: "Requests rejected due to invalid JSON payload.",
	})
)

func Register() {
	prometheus.MustRegister(
		RequestsTotal,
		RequestDuration,
		PublishErrors,
		BadRequests,
	)
}
