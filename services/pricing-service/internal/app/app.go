// dependency wiring
package app

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	goredis "github.com/redis/go-redis/v9"

	"ridepulse/pkg/events"
	"ridepulse/pkg/health"
	"ridepulse/services/pricing-service/internal/kafka"
	"ridepulse/services/pricing-service/internal/metrics"
	"ridepulse/services/pricing-service/internal/pricing"
)

const (
	publishTimeout  = 2 * time.Second
	shutdownTimeout = 5 * time.Second
	readyTimeout    = 1 * time.Second

	// driverLocationsKey holds free drivers only; see driver-simulator and
	// matching-service, which share it.
	driverLocationsKey = "drivers:locations"
)

// Run serves metrics and consumes RideRequested events until ctx is
// cancelled, then shuts everything down.
func Run(ctx context.Context, cfg Config) error {
	metrics.Register()

	// Prometheus scrape endpoint on its own port. Uses a dedicated ServeMux so
	// it can't collide with anything registered on http.DefaultServeMux.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.Handle("/health", health.Liveness())
	mux.Handle("/ready", health.Readiness(readyTimeout, map[string]health.Check{
		"kafka": health.Kafka(cfg.KafkaBrokers),
	}))
	server := &http.Server{
		Addr:         cfg.MetricsAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("pricing-service metrics listening on %s", cfg.MetricsAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("pricing-service metrics server: %v", err)
		}
	}()

	// Surge reads the number of free drivers once per tick, never per ride, so a
	// Redis problem can't slow pricing down.
	surge := pricing.NewSurgeTracker()
	rdb := goredis.NewClient(&goredis.Options{Addr: cfg.RedisAddr, ContextTimeoutEnabled: true})
	surgeCtx, stopSurge := context.WithCancel(ctx)
	surgeDone := make(chan struct{})
	go func() {
		defer close(surgeDone)
		pricing.RunSurge(surgeCtx, surge, func(c context.Context) (int64, error) {
			free, err := rdb.ZCard(c, driverLocationsKey).Result()
			metrics.SurgeMultiplier.Set(surge.Current())
			return free, err
		}, pricing.SurgeTickInterval)
	}()

	consumer := kafka.NewKafkaConsumer(
		cfg.KafkaBrokers,
		"pricing-service",
		"ride.requested",
		cfg.Workers,
	)
	publisher := kafka.NewKafkaPublisher(cfg.KafkaBrokers)

	runErr := consumer.Run(ctx, func(ctx context.Context, event events.RideRequestedEvent) error {
		// Counts handler attempts (retries included); pricing_retries_total
		// counts the retries among them.
		metrics.EventsConsumed.Inc()
		start := time.Now()
		defer func() {
			metrics.ProcessingDuration.Observe(time.Since(start).Seconds())
		}()

		surge.RecordRide()
		priced := pricing.ComputePrice(event, surge.Current())
		pubCtx, cancel := context.WithTimeout(ctx, publishTimeout)
		defer cancel()
		if err := publisher.PublishRidePriced(pubCtx, priced); err != nil {
			metrics.PublishErrors.Inc()
			return err
		}
		metrics.EventsPublished.Inc()
		return nil
	})

	stopSurge()
	<-surgeDone
	if err := rdb.Close(); err != nil {
		log.Printf("pricing-service redis close: %v", err)
	}

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
