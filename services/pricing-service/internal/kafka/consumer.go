package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"time"

	"github.com/segmentio/kafka-go"

	"ridepulse/pkg/kafkaconsumer"
	"ridepulse/services/pricing-service/internal/domain"
	"ridepulse/services/pricing-service/internal/metrics"
)

const dlqTopic = "ride.requested.dlq"

type KafkaConsumer struct {
	reader *kafka.Reader
	dlq    *kafka.Writer
}

func NewKafkaConsumer(brokers []string, groupId, topic string) *KafkaConsumer {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		GroupID: groupId,
		Topic:   topic,
		// Commits are issued by the runner after processing; the interval
		// makes them asynchronous batches instead of one round trip each.
		CommitInterval: time.Second,
		// With interval commits CommitMessages only queues; real commit errors
		// surface here and would otherwise be dropped silently.
		ErrorLogger: kafka.LoggerFunc(func(format string, args ...interface{}) {
			log.Printf("pricing-service kafka: "+format, args...)
		}),
		MinBytes: 1,
		MaxBytes: 10e6,
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
	return &KafkaConsumer{reader: reader, dlq: dlq}
}

// Run consumes RideRequested events until ctx is cancelled, calling handler
// for each. A decode failure is dead-lettered without retries; a handler
// error is retried and then dead-lettered. Offsets are committed only once a
// message has been handled or dead-lettered.
func (c *KafkaConsumer) Run(
	ctx context.Context,
	handler func(ctx context.Context, event domain.RideRequestedEvent) error,
) error {
	cfg := kafkaconsumer.Config{
		Workers:  runtime.NumCPU(),
		DLQ:      c.dlq,
		DLQTopic: dlqTopic,
		OnRetry:  func(kafka.Message, int, error) { metrics.Retries.Inc() },
		OnDLQ:    func(kafka.Message, error) { metrics.DLQMessages.Inc() },
	}
	return kafkaconsumer.Run(ctx, c.reader, cfg, func(ctx context.Context, msg kafka.Message) error {
		var event domain.RideRequestedEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			return kafkaconsumer.Permanent(fmt.Errorf("decode RideRequested: %w", err))
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
