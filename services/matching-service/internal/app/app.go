package app

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"ridepulse/services/matching-service/internal/domain"
	"ridepulse/services/matching-service/internal/kafka"
	"ridepulse/services/matching-service/internal/matching"
	"ridepulse/services/matching-service/internal/metrics"
	"ridepulse/services/matching-service/internal/redis"
)

// Deadlines for the per-ride processing pipeline.
//
// defaultMatchDeadline is the default total wall-clock budget the matcher has
// for finding a driver (Config.MatchDeadline, MATCH_DEADLINE). It must be
// strictly greater than matching.BatchDeadline so that at least one batch
// attempt fits inside it. A successful match's p95 was ~80 ms, so 500 ms
// still fits two full batches and halves the time a worker is held on a
// failure compared with the previous 800 ms.
//
// publishDeadline is a SEPARATE budget for emitting ride.matched to Kafka.
// It was previously sharing the match deadline via a single 300 ms context,
// which meant a match that took 250 ms left publish with 50 ms — often not
// enough for a Kafka WriteMessages round-trip with RequireOne, and the
// "match succeeded but publish timed out" failure mode was silent.
const (
	defaultMatchDeadline = 500 * time.Millisecond
	publishDeadline      = 300 * time.Millisecond

	shutdownTimeout = 5 * time.Second
)

// Run serves metrics and consumes RidePriced events until ctx is cancelled,
// then shuts everything down.
func Run(ctx context.Context, cfg Config) error {
	metrics.Register()

	// Dedicated mux so the scrape endpoint can't collide with anything
	// registered on http.DefaultServeMux.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	server := &http.Server{
		Addr:         cfg.MetricsAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("Prometheus metrics running on %s", cfg.MetricsAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("matching-service metrics server: %v", err)
		}
	}()

	// --- Dependencies ---
	driverRepo := redis.NewDriverRepository(cfg.RedisAddr)
	matcher := matching.New(driverRepo)

	consumer := kafka.NewKafkaConsumer(cfg.KafkaBrokers, cfg.MatchWorkers)
	publisher := kafka.NewKafkaPublisher(cfg.KafkaBrokers)

	runErr := consumer.Run(ctx, func(ctx context.Context, event domain.RidePricedEvent) error {
		matchCtx, matchCancel := context.WithTimeout(ctx, cfg.MatchDeadline)
		defer matchCancel()

		match, err := matcher.Match(matchCtx, event)
		if err != nil {
			if isBusinessFailure(err, matchCtx.Err()) {
				// No driver / match deadline is a business outcome, not a
				// retryable failure: log it and commit the offset.
				log.Printf("matching failed for ride %s: %v", event.RideID, err)
				return nil
			}
			// Redis/infra error: return it so the runner retries (and then
			// dead-letters). Retries are safe: Match replays an existing
			// assignment via ride:driver instead of matching again.
			return err
		}

		// Publish gets its OWN budget derived from the handler ctx, not from
		// matchCtx. A match that used most of the match deadline must not starve
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

// isBusinessFailure reports whether a Match error is a business outcome
// (commit and move on) rather than an infrastructure failure (retry).
// DeadlineExceeded counts only when the outer match deadline expired
// (matchCtxErr != nil); any other deadline, such as the detached assign
// timeout or a pool wait, is retried, which is safe via ride:driver replay.
func isBusinessFailure(err, matchCtxErr error) bool {
	return errors.Is(err, matching.ErrNoDriversAvailable) ||
		(errors.Is(err, context.DeadlineExceeded) && matchCtxErr != nil)
}
