package app

import (
	"context"
	"log"
	"math/rand"
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
)

func Run() error {
	metrics.Register()
	go func() {
	http.Handle("/metrics", promhttp.Handler())
	log.Println("Prometheus metrics running on :2112")

	server := &http.Server{
		Addr:         ":2112",
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	server.ListenAndServe()
}()


	ctx := context.Background()

	// --- Dependencies ---
	driverRepo := redis.NewDriverRepository("localhost:6379")
	matcher := matching.New(driverRepo)

	consumer := kafka.NewKafkaConsumer([]string{"localhost:9092"})
	publisher := kafka.NewKafkaPublisher([]string{"localhost:9092"})
	rand.Seed(time.Now().UnixNano())
	// --- Worker pool ---
	pool := NewWorkerPool(200)

	pool.Start(ctx, func(event domain.RidePricedEvent) error {
		matchCtx, matchCancel := context.WithTimeout(ctx, matchDeadline)
		defer matchCancel()

		match, err := matcher.Match(matchCtx, event)
		if err != nil {
			log.Printf("matching failed for ride %s: %v", event.RideID, err)
			return err
		}

		// Publish gets its OWN budget derived from the pool's ctx, not from
		// matchCtx. A match that used most of matchDeadline must not starve
		// the Kafka write of its round-trip budget.
		publishCtx, publishCancel := context.WithTimeout(ctx, publishDeadline)
		defer publishCancel()
		return publisher.PublishRideMatched(publishCtx, match)
	})

	// --- Kafka consumer → enqueue ---
	return consumer.ConsumeRidePricedEvent(func(event domain.RidePricedEvent) error {
		pool.Submit(event)
		return nil
	})
}
