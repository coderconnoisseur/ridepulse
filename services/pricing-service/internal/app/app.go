// dependency wiring
package app

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"ridepulse/services/pricing-service/internal/domain"
	"ridepulse/services/pricing-service/internal/kafka"
	"ridepulse/services/pricing-service/internal/metrics"
	"ridepulse/services/pricing-service/internal/pricing"
)

const metricsAddr = ":2115"

func Run() error {
	metrics.Register()

	// Prometheus scrape endpoint on its own port. Uses a dedicated ServeMux so
	// it can't collide with anything registered on http.DefaultServeMux.
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		server := &http.Server{
			Addr:         metricsAddr,
			Handler:      mux,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		}
		log.Printf("pricing-service metrics listening on %s", metricsAddr)
		_ = server.ListenAndServe()
	}()

	ctx := context.Background() // TODO: wire real graceful shutdown

	consumer := kafka.NewKafkaConsumer(
		[]string{"localhost:9092"},
		"pricing-service",
		"ride.requested",
	)
	publisher := kafka.NewKafkaPublisher([]string{"localhost:9092"})

	pool := NewWorkerPool(100)
	pool.Start(ctx, func(event domain.RideRequestedEvent) error {
		start := time.Now()
		defer func() {
			metrics.ProcessingDuration.Observe(time.Since(start).Seconds())
		}()

		priced := pricing.ComputePrice(event)
		if err := publisher.PublishRidePriced(ctx, priced); err != nil {
			metrics.PublishErrors.Inc()
			return err
		}
		metrics.EventsPublished.Inc()
		return nil
	})

	return consumer.ConsumeRideRequested(func(event domain.RideRequestedEvent) error {
		metrics.EventsConsumed.Inc()
		pool.Enqueue(event)
		return nil
	})
}
