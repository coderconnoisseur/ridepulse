package kafka

import (
	"context"
	// "fmt"
	"ridepulse/services/matching-service/internal/domain"
	"encoding/json"
	"github.com/segmentio/kafka-go"
)
type KafkaConsumer struct{
	reader *kafka.Reader
}
func NewKafkaConsumer (brokers []string) *KafkaConsumer{
	r:=kafka.NewReader(
		kafka.ReaderConfig{
			Brokers:brokers,
			GroupID: "matching-service",
			Topic: "ride.priced",
		})
		return &KafkaConsumer{reader:r}
}

func (c *KafkaConsumer) ConsumeRidePricedEvent(
	handler func (event domain.RidePricedEvent) error,)error{
		for{
			msg,err:=c.reader.FetchMessage(context.Background())
			if err!=nil{
				return err
			}
			var event domain.RidePricedEvent
			if err:=json.Unmarshal(msg.Value,&event);
			err!=nil{
				c.reader.CommitMessages(context.Background(),msg)
				continue
			}
			if err:=handler(event);err!=nil{
				continue
			}
			if err:=c.reader.CommitMessages(context.Background(),msg);err!=nil{
				return err}
		}
	}