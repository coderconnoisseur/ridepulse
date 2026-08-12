package app

import (
	"time"

	"ridepulse/services/assignment-service/internal/domain"
	"ridepulse/services/assignment-service/internal/httpapi"
	"ridepulse/services/assignment-service/internal/kafka"
	"ridepulse/services/assignment-service/internal/metrics"
	"ridepulse/services/assignment-service/internal/store"
)

const (
	kafkaBroker = "localhost:9092"
	httpAddr    = ":2114"
)

func Run() error {
	metrics.Register()
	mem := store.NewMemory()

	go func() { _ = httpapi.Serve(httpAddr, mem) }()

	consumer := kafka.NewKafkaConsumer([]string{kafkaBroker})
	return consumer.ConsumeRideMatched(func(event domain.RideMatchedEvent) error {
		var latencyMs int64
		if event.RequestedAt > 0 {
			latencyMs = (time.Now().UnixNano() - event.RequestedAt) / int64(time.Millisecond)
			metrics.EndToEndLatency.Observe(float64(latencyMs) / 1000.0)
		} else {
			metrics.MissingRequestedAt.Inc()
		}
		mem.Put(domain.Assignment{
			RideID:    event.RideID,
			DriverID:  event.DriverID,
			MatchedAT: event.MatchedAT,
			LatencyMs: latencyMs,
		})
		metrics.AssignmentsTotal.Inc()
		return nil
	})
}
