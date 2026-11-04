package kafka

import (
	"context"
	"encoding/json"
	"github.com/segmentio/kafka-go"
	"ridepulse/pkg/events"
	"time"
)

// publishBatchWindow is how long the writer collects messages into a batch. It
// must exceed a broker round trip: kafka-go closes batches on this timer but
// sends them one at a time per partition, so a shorter window builds a queue
// that grows without limit under load.
const publishBatchWindow = 25 * time.Millisecond

type KafkaPublisher struct {
	writer *kafka.Writer
}

func NewKafkaPublisher(brokers []string) *KafkaPublisher {
	writer := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        events.TopicRidePriced,
		Balancer:     &kafka.Hash{},
		RequiredAcks: kafka.RequireOne,
		BatchTimeout: publishBatchWindow,
	}
	return &KafkaPublisher{writer}
}

func (p *KafkaPublisher) PublishRidePriced(
	ctx context.Context,
	event events.RidePricedEvent,
) error {
	val, err := json.Marshal(event)
	if err != nil {
		return err
	}
	msg := kafka.Message{
		Key:   []byte(event.RideID), //kafka only understands byte arrays , (language agnostic)
		Value: val,
	}
	return p.writer.WriteMessages(ctx, msg)
}

// Close flushes and closes the underlying writer.
func (p *KafkaPublisher) Close() error {
	return p.writer.Close()
}
