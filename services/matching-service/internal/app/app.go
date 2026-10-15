package app

import (
	"context"
	"log"
	"errors"
	"time"

	"ridepulse/services/matching-service/internal/metrics"
	"ridepulse/services/matching-service/internal/domain"
	"ridepulse/services/matching-service/internal/kafka"
	"ridepulse/services/matching-service/internal/matching"
	"ridepulse/services/matching-service/internal/redis"
	"net/http"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Deadlines for the per-ride processing pipeline.
//
// matchDeadline is the total wall-clock budget the matcher has for finding a
// driver. It must be strictly greater than matching.BatchDeadline so that at
// least one batch attempt fits inside it. In practice we size it as
// roughly (matching.BatchDeadline * desiredBatchAttempts) so contention on a
// hotspot can traverse multiple batches before giving up.
//
// publishDeadline is a SEPARATE budget for emitting ride.matched to Kafka.
// It was previously sharing the match deadline via a single 300 ms context,
// which meant a match that took 250 ms left publish with 50 ms — often not
// enough for a Kafka WriteMessages round-trip with RequireOne, and the
// "match succeeded but publish timed out" failure mode was silent.
const (
	matchDeadline   = 800 * time.Millisecond
	publishDeadline = 300 * time.Millisecond

	metricsAddr     = ":2112"
	shutdownTimeout = 5 * time.Second
)

// Run serves metrics and consumes RidePriced events until ctx is cancelled,
// then shuts everything down.
func Run(ctx context.Context) error {
	metrics.Register()

	// Dedicated mux so the scrape endpoint can't collide with anything
	// registered on http.DefaultServeMux.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	server := &http.Server{
		Addr:         metricsAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("Prometheus metrics running on %s", metricsAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("matching-service metrics server: %v", err)
		}
	}()

	// --- Dependencies ---
	driverRepo := redis.NewDriverRepository("localhost:6379")
	matcher := matching.New(driverRepo)

	consumer := kafka.NewKafkaConsumer([]string{"localhost:9092"})
	publisher := kafka.NewKafkaPublisher([]string{"localhost:9092"})

	runErr := consumer.Run(ctx, func(ctx context.Context, event domain.RidePricedEvent) error {
		matchCtx, matchCancel := context.WithTimeout(ctx, matchDeadline)
		defer matchCancel()

		match, err := matcher.Match(matchCtx, event)
		if err != nil {
			// No driver / deadline is a business outcome, not a retryable
			// failure: log it and commit the offset.
			log.Printf("matching failed for ride %s: %v", event.RideID, err)
			return nil
		}

		// Publish gets its OWN budget derived from the handler ctx, not from
		// matchCtx. A match that used most of matchDeadline must not starve
		// the Kafka write of its round-trip budget. A publish failure is
		// returned so the message is retried and then dead-lettered.
		publishCtx, publishCancel := context.WithTimeout(ctx, publishDeadline)
		defer publishCancel()
		return publisher.PublishRideMatched(publishCtx, match)
	})

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("matching-service metrics shutdown: %v", err)
	}
	if err := consumer.Close(); err != nil {
		log.Printf("matching-service consumer close: %v", err)
	}
	if err := publisher.Close(); err != nil {
		log.Printf("matching-service publisher close: %v", err)
	}
	if err := driverRepo.Close(); err != nil {
		log.Printf("matching-service redis close: %v", err)
	}
	log.Printf("matching-service stopped")
	return runErr
}
