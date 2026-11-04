package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/segmentio/kafka-go"

	"ridepulse/pkg/events"
)

// publishBatchWindow is how long the writer collects messages into a batch. It
// must exceed a broker round trip: kafka-go closes batches on this timer but
// sends them one at a time per partition, so a shorter window builds a queue
// that grows without limit under load.
const publishBatchWindow = 25 * time.Millisecond

// KafkaPublisher writes ride.requested events. It hides the kafka-go writer
// behind the EventPublisher port.
type KafkaPublisher struct {
	Writer *kafka.Writer
}

func NewKafkaPublisher(brokers []string) *KafkaPublisher {
	writer := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        events.TopicRideRequested,
		Balancer:     &kafka.Hash{}, // same ride ID always lands on the same partition
		RequiredAcks: kafka.RequireOne,
		BatchTimeout: publishBatchWindow,
		BatchSize:    100,
		Async:        false,
	}
	return &KafkaPublisher{Writer: writer}
}

// PublishRideRequested writes the event keyed by ride ID.
func (p *KafkaPublisher) PublishRideRequested(ctx context.Context, event events.RideRequestedEvent) error {
	value, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return p.Writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(event.RideID),
		Value: value,
	})
}
