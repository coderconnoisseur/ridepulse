package app

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
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

// Run serves the HTTP API and consumes RideMatched and RideUnmatched events
// until ctx is cancelled, then shuts everything down.
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

	matched := kafka.NewKafkaConsumer(cfg.KafkaBrokers)
	unmatched := kafka.NewUnmatchedConsumer(cfg.KafkaBrokers)

	// Both consumers share a derived ctx: if one fails fatally the other is
	// cancelled, and the first error is returned once both have stopped.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	runner := func(run func(context.Context) error) {
		defer wg.Done()
		if err := run(runCtx); err != nil {
			errOnce.Do(func() { firstErr = err })
			cancelRun()
		}
	}
	wg.Add(2)
	go runner(func(c context.Context) error { return matched.Run(c, handleMatched(mem)) })
	go runner(func(c context.Context) error { return unmatched.Run(c, handleUnmatched(mem)) })
	wg.Wait()
	runErr := firstErr

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("assignment-service http shutdown: %v", err)
	}
	if err := matched.Close(); err != nil {
		log.Printf("assignment-service matched consumer close: %v", err)
	}
	if err := unmatched.Close(); err != nil {
		log.Printf("assignment-service unmatched consumer close: %v", err)
	}
	log.Printf("assignment-service stopped")
	return runErr
}

// handleMatched records a matched ride. Metrics are observed only for the
// first delivery; redeliveries count as duplicates.
func handleMatched(mem *store.Memory) func(context.Context, domain.RideMatchedEvent) error {
	return func(_ context.Context, event domain.RideMatchedEvent) error {
		var latencyMs int64
		if event.RequestedAt > 0 {
			latencyMs = (time.Now().UnixNano() - event.RequestedAt) / int64(time.Millisecond)
		}
		isNew := mem.PutMatched(domain.Assignment{
			RideID:    event.RideID,
			DriverID:  event.DriverID,
			MatchedAT: event.MatchedAT,
			LatencyMs: latencyMs,
		})
		if !isNew {
			metrics.AssignmentDuplicates.Inc()
			return nil
		}
		if event.RequestedAt > 0 {
			metrics.EndToEndLatency.Observe(float64(latencyMs) / 1000.0)
		} else {
			metrics.MissingRequestedAt.Inc()
		}
		metrics.AssignmentsTotal.Inc()
		return nil
	}
}

// handleUnmatched records a failed ride, ignoring duplicates and rides that
// already have an outcome.
func handleUnmatched(mem *store.Memory) func(context.Context, domain.RideUnmatchedEvent) error {
	return func(_ context.Context, event domain.RideUnmatchedEvent) error {
		if !mem.PutUnmatched(event.RideID, event.Reason, event.FailedAt) {
			metrics.UnmatchedDuplicates.Inc()
			return nil
		}
		metrics.UnmatchedTotal.WithLabelValues(event.Reason).Inc()
		if event.RequestedAt > 0 {
			elapsed := time.Now().UnixNano() - event.RequestedAt
			metrics.UnmatchedEndToEndLatency.Observe(float64(elapsed) / float64(time.Second))
		}
		return nil
	}
}
