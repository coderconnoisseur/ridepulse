package app

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"ridepulse/pkg/events"
	"ridepulse/pkg/health"
	"ridepulse/services/matching-service/internal/kafka"
	"ridepulse/services/matching-service/internal/matching"
	"ridepulse/services/matching-service/internal/metrics"
	"ridepulse/services/matching-service/internal/redis"
)

// publishDeadline bounds one Kafka write. It must comfortably exceed the writer's
// batch window plus a broker round trip: a publish that times out can still
// reach the broker, so a tight budget turns slow writes into retries and
// duplicate events, which add more load.
const (
	publishDeadline = 2 * time.Second

	shutdownTimeout = 5 * time.Second
	// readyTimeout bounds the whole /ready probe (Kafka dial + Redis PING).
	readyTimeout = 1 * time.Second
)

// Run serves metrics and consumes RidePriced events until ctx is cancelled,
// then shuts everything down.
func Run(ctx context.Context, cfg Config) error {
	metrics.Register()

	// --- Dependencies ---
	driverRepo := redis.NewDriverRepository(cfg.RedisAddr, cfg.RedisPoolSize)
	matcher := matching.New(driverRepo, cfg.MatchDeadline)

	// Dedicated mux so the scrape and probe endpoints can't collide with
	// anything registered on http.DefaultServeMux.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.Handle("/health", health.Liveness())
	mux.Handle("/ready", health.Readiness(readyTimeout, map[string]health.Check{
		"kafka": health.Kafka(cfg.KafkaBrokers),
		"redis": driverRepo.Ping,
	}))
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

	consumer := kafka.NewKafkaConsumer(cfg.KafkaBrokers, cfg.MatchWorkers)
	publisher := kafka.NewKafkaPublisher(cfg.KafkaBrokers)

	runErr := consumer.Run(ctx, newHandler(matcher, publisher))

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

// matcherAPI and eventPublisher are the handler's collaborators, narrowed so
// the handler can be tested with fakes.
type matcherAPI interface {
	Match(ctx context.Context, event events.RidePricedEvent) (events.RideMatchedEvent, error)
}

type eventPublisher interface {
	PublishRideMatched(ctx context.Context, event events.RideMatchedEvent) error
	PublishRideUnmatched(ctx context.Context, event events.RideUnmatchedEvent) error
}

// newHandler builds the per-ride handler: match, then publish ride.matched,
// or if no driver is free publish ride.unmatched so the rider is told. Any
// error is returned so the runner retries and then dead-letters the message.
// Retries are safe: Match replays an existing assignment instead of matching
// again.
func newHandler(matcher matcherAPI, publisher eventPublisher) func(context.Context, events.RidePricedEvent) error {
	return func(ctx context.Context, event events.RidePricedEvent) error {
		match, err := matcher.Match(ctx, event)
		if errors.Is(err, matching.ErrNoDriversAvailable) {
			log.Printf("no driver for ride %s", event.RideID)
			publishCtx, publishCancel := context.WithTimeout(ctx, publishDeadline)
			defer publishCancel()
			if perr := publisher.PublishRideUnmatched(publishCtx, events.RideUnmatchedEvent{
				Version:     events.Version,
				RideID:      event.RideID,
				Reason:      events.ReasonNoDrivers,
				RequestedAt: event.RequestedAt,
				FailedAt:    time.Now().UnixNano(),
			}); perr != nil {
				return perr
			}
			metrics.MatchUnmatchedPublished.WithLabelValues(events.ReasonNoDrivers).Inc()
			return nil
		}
		if err != nil {
			return err
		}

		publishCtx, publishCancel := context.WithTimeout(ctx, publishDeadline)
		defer publishCancel()
		return publisher.PublishRideMatched(publishCtx, match)
	}
}
