package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/segmentio/kafka-go"

	"ridepulse/pkg/events"
	"ridepulse/pkg/kafkaconsumer"
	"ridepulse/services/assignment-service/internal/metrics"
)

const (
	// Each topic gets its own consumer group. With one shared group, the
	// group leader's metadata request for both topics fails as a whole while
	// ride.unmatched does not exist yet (it is created lazily on the first
	// business failure), leaving both readers without partitions.
	matchedGroupID   = "assignment-service"
	unmatchedGroupID = "assignment-service-unmatched"

	matchedTopic      = events.TopicRideMatched
	matchedDLQTopic   = "ride.matched.dlq"
	unmatchedTopic    = events.TopicRideUnmatched
	unmatchedDLQTopic = "ride.unmatched.dlq"
)

// consumer is the topic-agnostic reader + DLQ writer pair shared by the
// typed consumers below. Each topic has its own consumer group.
type consumer struct {
	reader   *kafka.Reader
	dlq      *kafka.Writer
	dlqTopic string
}

func newConsumer(brokers []string, groupID, topic, dlqTopic string) *consumer {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		GroupID: groupID,
		Topic:   topic,
		// Commits are issued by the runner after processing; the interval
		// makes them asynchronous batches instead of one round trip each.
		CommitInterval: time.Second,
		// With interval commits CommitMessages only queues; real commit errors
		// surface here and would otherwise be dropped silently.
		ErrorLogger: kafka.LoggerFunc(func(format string, args ...interface{}) {
			log.Printf("assignment-service kafka: "+format, args...)
		}),
	})
	// No fixed Topic: the runner sets the topic on each DLQ message.
	dlq := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Balancer:     &kafka.Hash{},
		RequiredAcks: kafka.RequireOne,
		// DLQ topics are not provisioned elsewhere; create on first write.
		AllowAutoTopicCreation: true,
		// Default batch timeout (1s) would stall every synchronous DLQ write.
		BatchTimeout: 10 * time.Millisecond,
	}
	return &consumer{reader: r, dlq: dlq, dlqTopic: dlqTopic}
}

// run consumes events of type T until ctx is cancelled, calling handler for
// each. A decode failure is dead-lettered without retries; a handler error is
// retried and then dead-lettered. Offsets are committed only once a message
// has been handled or dead-lettered.
func run[T any](
	ctx context.Context,
	c *consumer,
	what string,
	handler func(ctx context.Context, event T) error,
) error {
	cfg := kafkaconsumer.Config{
		Workers:  4,
		DLQ:      c.dlq,
		DLQTopic: c.dlqTopic,
		OnDLQ:    func(kafka.Message, error) { metrics.DLQMessages.Inc() },
	}
	return kafkaconsumer.Run(ctx, c.reader, cfg, func(ctx context.Context, msg kafka.Message) error {
		var event T
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			return kafkaconsumer.Permanent(fmt.Errorf("decode %s: %w", what, err))
		}
		return handler(ctx, event)
	})
}

// Close closes the reader and the DLQ writer.
func (c *consumer) Close() error {
	rerr := c.reader.Close()
	if err := c.dlq.Close(); err != nil {
		return err
	}
	return rerr
}

// KafkaConsumer consumes ride.matched.
type KafkaConsumer struct{ *consumer }

func NewKafkaConsumer(brokers []string) *KafkaConsumer {
	return &KafkaConsumer{newConsumer(brokers, matchedGroupID, matchedTopic, matchedDLQTopic)}
}

// Run consumes RideMatched events until ctx is cancelled.
func (c *KafkaConsumer) Run(
	ctx context.Context,
	handler func(ctx context.Context, event events.RideMatchedEvent) error,
) error {
	return run(ctx, c.consumer, "RideMatched", handler)
}

// UnmatchedConsumer consumes ride.unmatched.
type UnmatchedConsumer struct{ *consumer }

func NewUnmatchedConsumer(brokers []string) *UnmatchedConsumer {
	return &UnmatchedConsumer{newConsumer(brokers, unmatchedGroupID, unmatchedTopic, unmatchedDLQTopic)}
}

// Run consumes RideUnmatched events until ctx is cancelled.
func (c *UnmatchedConsumer) Run(
	ctx context.Context,
	handler func(ctx context.Context, event events.RideUnmatchedEvent) error,
) error {
	return run(ctx, c.consumer, "RideUnmatched", handler)
}
