package app

import (
	"context"
	"log"
	"time"

	"ridepulse/services/matching-service/internal/domain"
	"ridepulse/services/matching-service/internal/kafka"
	"ridepulse/services/matching-service/internal/matching"
	"ridepulse/services/matching-service/internal/redis"
)

func Run() error {

	ctx := context.Background()

	// --- Dependencies ---
	driverRepo := redis.NewDriverRepository("localhost:6379")
	matcher := matching.New(driverRepo)

	consumer := kafka.NewKafkaConsumer([]string{"localhost:9092"})
	publisher := kafka.NewKafkaPublisher([]string{"localhost:9092"})

	// --- Worker pool ---
	pool := NewWorkerPool(1000)

	pool.Start(ctx, func(event domain.RidePricedEvent) error {
		jobctx,cancel:=context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		match, err := matcher.Match(jobctx,event)
		if err != nil {
			log.Printf("matching failed for ride %s: %v", event.RideID, err)
			return err
		}

		return publisher.PublishRideMatched(jobctx, match)
	})

	// --- Kafka consumer → enqueue ---
	return consumer.ConsumeRidePricedEvent(func(event domain.RidePricedEvent) error {
		pool.Submit(event)
		return nil
	})
}
