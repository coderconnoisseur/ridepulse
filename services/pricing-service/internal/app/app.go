// dependency wiring
package app

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"ridepulse/services/pricing-service/internal/domain"
	"ridepulse/services/pricing-service/internal/kafka"
	"ridepulse/services/pricing-service/internal/metrics"
	"ridepulse/services/pricing-service/internal/pricing"
)

const (
	metricsAddr     = ":2115"
	publishTimeout  = 300 * time.Millisecond
	shutdownTimeout = 5 * time.Second
)

// Run serves metrics and consumes RideRequested events until ctx is
// cancelled, then shuts everything down.
func Run(ctx context.Context) error {
	metrics.Register()

	// Prometheus scrape endpoint on its own port. Uses a dedicated ServeMux so
	// it can't collide with anything registered on http.DefaultServeMux.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	server := &http.Server{
		Addr:         metricsAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("pricing-service metrics listening on %s", metricsAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("pricing-service metrics server: %v", err)
		}
	}()

	consumer := kafka.NewKafkaConsumer(
		[]string{"localhost:9092"},
		"pricing-service",
		"ride.requested",
	)
	publisher := kafka.NewKafkaPublisher([]string{"localhost:9092"})

	runErr := consumer.Run(ctx, func(ctx context.Context, event domain.RideRequestedEvent) error {
		// Counts handler attempts (retries included); pricing_retries_total
		// counts the retries among them.
		metrics.EventsConsumed.Inc()
		start := time.Now()
		defer func() {
			metrics.ProcessingDuration.Observe(time.Since(start).Seconds())
		}()

		priced := pricing.ComputePrice(event)
		pubCtx, cancel := context.WithTimeout(ctx, publishTimeout)
		defer cancel()
		if err := publisher.PublishRidePriced(pubCtx, priced); err != nil {
			metrics.PublishErrors.Inc()
			return err
		}
		metrics.EventsPublished.Inc()
		return nil
	})

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("pricing-service metrics shutdown: %v", err)
	}
	if err := consumer.Close(); err != nil {
		log.Printf("pricing-service consumer close: %v", err)
	}
	if err := publisher.Close(); err != nil {
		log.Printf("pricing-service publisher close: %v", err)
	}
	log.Printf("pricing-service stopped")
	return runErr
}
