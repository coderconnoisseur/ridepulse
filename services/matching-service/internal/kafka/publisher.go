package kafka

import (
	"context"
	"encoding/json"
	"errors"
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
	writer          *kafka.Writer
	unmatchedWriter *kafka.Writer
}

func NewKafkaPublisher(brokers []string) *KafkaPublisher {
	writer := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        events.TopicRideMatched,
		Balancer:     &kafka.Hash{},
		RequiredAcks: kafka.RequireOne,
		BatchTimeout: publishBatchWindow,
	}
	unmatched := &kafka.Writer{
		Addr:                   kafka.TCP(brokers...),
		Topic:                  events.TopicRideUnmatched,
		Balancer:               &kafka.Hash{},
		RequiredAcks:           kafka.RequireOne,
		BatchTimeout:           50_000_000, //50ms
		AllowAutoTopicCreation: true,       // new topic
	}
	return &KafkaPublisher{writer, unmatched}
}

func (p *KafkaPublisher) PublishRideMatched(
	ctx context.Context,
	event events.RideMatchedEvent,
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

// PublishRideUnmatched writes a ride.unmatched event keyed by ride ID.
func (p *KafkaPublisher) PublishRideUnmatched(
	ctx context.Context,
	event events.RideUnmatchedEvent,
) error {
	val, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return p.unmatchedWriter.WriteMessages(ctx, kafka.Message{
		Key:   []byte(event.RideID),
		Value: val,
	})
}

// Close flushes and closes both writers.
func (p *KafkaPublisher) Close() error {
	return errors.Join(p.writer.Close(), p.unmatchedWriter.Close())
}
