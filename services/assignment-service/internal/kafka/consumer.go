package kafka

import (
	"context"
	"encoding/json"

	"github.com/segmentio/kafka-go"

	"ridepulse/services/assignment-service/internal/domain"
)

type KafkaConsumer struct {
	reader *kafka.Reader
}

func NewKafkaConsumer(brokers []string) *KafkaConsumer {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		GroupID: "assignment-service",
		Topic:   "ride.matched",
	})
	return &KafkaConsumer{reader: r}
}

func (c *KafkaConsumer) ConsumeRideMatched(
	handler func(event domain.RideMatchedEvent) error,
) error {
	for {
		msg, err := c.reader.FetchMessage(context.Background())
		if err != nil {
			return err
		}
		var event domain.RideMatchedEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			// Malformed message → commit and move on so we don't block the topic.
			_ = c.reader.CommitMessages(context.Background(), msg)
			continue
		}
		if err := handler(event); err != nil {
			// Match the pattern used in pricing- and matching-service consumers
			// (silent skip on handler error). Documented as a broader follow-up
			// in the benchmarking runbook — kept consistent for now so behaviour
			// is uniform across the fleet during load tests.
			_ = c.reader.CommitMessages(context.Background(), msg)
			continue
		}
		if err := c.reader.CommitMessages(context.Background(), msg); err != nil {
			return err
		}
	}
}
