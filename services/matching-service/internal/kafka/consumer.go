package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"time"

	"github.com/segmentio/kafka-go"

	"ridepulse/pkg/kafkaconsumer"
	"ridepulse/services/matching-service/internal/domain"
	"ridepulse/services/matching-service/internal/metrics"
)

const dlqTopic = "ride.priced.dlq"

type KafkaConsumer struct {
	reader *kafka.Reader
	dlq    *kafka.Writer
}

func NewKafkaConsumer(brokers []string) *KafkaConsumer {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		GroupID: "matching-service",
		Topic:   "ride.priced",
		// Commits are issued by the runner after processing; the interval
		// makes them asynchronous batches instead of one round trip each.
		CommitInterval: time.Second,
	})
	// No fixed Topic: the runner sets the topic on each DLQ message.
	dlq := &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Balancer:     &kafka.Hash{},
		RequiredAcks: kafka.RequireOne,
	}
	return &KafkaConsumer{reader: r, dlq: dlq}
}

// Run consumes RidePriced events until ctx is cancelled, calling handler for
// each. A decode failure is dead-lettered without retries; a handler error is
// retried and then dead-lettered. Offsets are committed only once a message
// has been handled or dead-lettered.
func (c *KafkaConsumer) Run(
	ctx context.Context,
	handler func(ctx context.Context, event domain.RidePricedEvent) error,
) error {
	cfg := kafkaconsumer.Config{
		Workers:  runtime.NumCPU() * 5,
		DLQ:      c.dlq,
		DLQTopic: dlqTopic,
		OnRetry:  func(kafka.Message, int, error) { metrics.Retries.Inc() },
		OnDLQ:    func(kafka.Message, error) { metrics.DLQMessages.Inc() },
	}
	return kafkaconsumer.Run(ctx, c.reader, cfg, func(ctx context.Context, msg kafka.Message) error {
		var event domain.RidePricedEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			return kafkaconsumer.Permanent(fmt.Errorf("decode RidePriced: %w", err))
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
