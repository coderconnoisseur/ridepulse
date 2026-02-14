package app

import (
	"context"
	"log"

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
	pool := NewWorkerPool(100)

	pool.Start(ctx, func(event domain.RidePricedEvent) error {

		match, err := matcher.Match(event)
		if err != nil {
			log.Printf("matching failed for ride %s: %v", event.RideID, err)
			return err
		}

		return publisher.PublishRideMatched(ctx, match)
	})

	// --- Kafka consumer → enqueue ---
	return consumer.ConsumeRidePricedEvent(func(event domain.RidePricedEvent) error {
		pool.Submit(event)
		return nil
	})
}
