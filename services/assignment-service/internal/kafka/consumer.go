package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/segmentio/kafka-go"

	"ridepulse/pkg/kafkaconsumer"
	"ridepulse/services/assignment-service/internal/domain"
	"ridepulse/services/assignment-service/internal/metrics"
)

const dlqTopic = "ride.matched.dlq"

type KafkaConsumer struct {
	reader *kafka.Reader
	dlq    *kafka.Writer
}

func NewKafkaConsumer(brokers []string) *KafkaConsumer {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		GroupID: "assignment-service",
		Topic:   "ride.matched",
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
	return &KafkaConsumer{reader: r, dlq: dlq}
}

// Run consumes RideMatched events until ctx is cancelled, calling handler for
// each. A decode failure is dead-lettered without retries; a handler error is
// retried and then dead-lettered. Offsets are committed only once a message
// has been handled or dead-lettered.
func (c *KafkaConsumer) Run(
	ctx context.Context,
	handler func(ctx context.Context, event domain.RideMatchedEvent) error,
) error {
	cfg := kafkaconsumer.Config{
		Workers:  4,
		DLQ:      c.dlq,
		DLQTopic: dlqTopic,
		OnDLQ:    func(kafka.Message, error) { metrics.DLQMessages.Inc() },
	}
	return kafkaconsumer.Run(ctx, c.reader, cfg, func(ctx context.Context, msg kafka.Message) error {
		var event domain.RideMatchedEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			return kafkaconsumer.Permanent(fmt.Errorf("decode RideMatched: %w", err))
		}
		return handler(ctx, event)
	})
}

// Close closes the reader and the DLQ writer.
func (c *KafkaConsumer) Close() error {
	rerr := c.reader.Close()
	if err := c.dlq.Close(); err != nil {
		return err
	}
	return rerr
}
