package app

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"ridepulse/services/assignment-service/internal/domain"
	"ridepulse/services/assignment-service/internal/httpapi"
	"ridepulse/services/assignment-service/internal/kafka"
	"ridepulse/services/assignment-service/internal/metrics"
	"ridepulse/services/assignment-service/internal/store"
)

const (
	shutdownTimeout = 5 * time.Second
)

// Run serves the HTTP API and consumes RideMatched events until ctx is
// cancelled, then shuts everything down.
func Run(ctx context.Context, cfg Config) error {
	metrics.Register()
	mem := store.NewMemory()

	server := httpapi.NewServer(cfg.HTTPAddr, mem)
	go func() {
		log.Printf("assignment-service listening on %s", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("assignment-service http server: %v", err)
		}
	}()

	consumer := kafka.NewKafkaConsumer(cfg.KafkaBrokers)
	runErr := consumer.Run(ctx, func(_ context.Context, event domain.RideMatchedEvent) error {
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("assignment-service http shutdown: %v", err)
	}
	if err := consumer.Close(); err != nil {
		log.Printf("assignment-service consumer close: %v", err)
	}
	log.Printf("assignment-service stopped")
	return runErr
}
