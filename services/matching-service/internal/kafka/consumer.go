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
	"ridepulse/services/matching-service/internal/metrics"
)

const dlqTopic = events.TopicRidePriced + ".dlq"

type KafkaConsumer struct {
	reader  *kafka.Reader
	dlq     *kafka.Writer
	workers int
}

func NewKafkaConsumer(brokers []string, workers int) *KafkaConsumer {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers: brokers,
		GroupID: "matching-service",
		Topic:   events.TopicRidePriced,
		// Commits are issued by the runner after processing; the interval
		// makes them asynchronous batches instead of one round trip each.
		CommitInterval: time.Second,
		// With interval commits CommitMessages only queues; real commit errors
		// surface here and would otherwise be dropped silently.
		ErrorLogger: kafka.LoggerFunc(func(format string, args ...interface{}) {
			log.Printf("matching-service kafka: "+format, args...)
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
	return &KafkaConsumer{reader: r, dlq: dlq, workers: workers}
}

// Run consumes RidePriced events until ctx is cancelled, calling handler for
// each. A decode failure is dead-lettered without retries; a handler error is
// retried and then dead-lettered. Offsets are committed only once a message
// has been handled or dead-lettered.
func (c *KafkaConsumer) Run(
	ctx context.Context,
	handler func(ctx context.Context, event events.RidePricedEvent) error,
) error {
	cfg := kafkaconsumer.Config{
		Workers:  c.workers,
		DLQ:      c.dlq,
		DLQTopic: dlqTopic,
		OnRetry:  func(kafka.Message, int, error) { metrics.Retries.Inc() },
		OnDLQ:    func(kafka.Message, error) { metrics.DLQMessages.Inc() },
	}
	return kafkaconsumer.Run(ctx, c.reader, cfg, func(ctx context.Context, msg kafka.Message) error {
		var event events.RidePricedEvent
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
