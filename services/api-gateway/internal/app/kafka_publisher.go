package app

import (
	"context"
	"encoding/json"
	"ridepulse/services/api-gateway/internal/domain"
	"time"

	"github.com/segmentio/kafka-go"
)
type KafkaPublisher struct {
	Writer *kafka.Writer
	//wrapper over kafka writer to implement EventPublisher interface
	// will hide details from rest of my application 
}

func NewKafkaPublisher(brokers []string) *KafkaPublisher {
	//kafka publisher constructor
//accepts list of broker string such as "localhost:9092"
	writer:=&kafka.Writer{
		Addr:kafka.TCP(brokers...),
		Topic:"ride.requested",
		Balancer: &kafka.Hash{},//i need consistent hashing based balancing strategy
		RequiredAcks: kafka.RequireOne,
		BatchTimeout: 10*time.Millisecond,
		BatchSize: 100,
		Async: false,
	}
	return &KafkaPublisher{Writer:writer}
}
func (p *KafkaPublisher) PublishRideRequested(//method of KafkaPublisher 
	ctx context.Context, event domain.RideRequestedEvent,
)error{
		value,err:=json.Marshal(event)// this will convert event struct to []byte
		if err!=nil{// in case error
			return err
		}
		msg:=kafka.Message{//write kafka message with key as rideid and val as other info
			Key: []byte(event.RideID),
			Value: value,
		}
		return p.Writer.WriteMessages(ctx,msg)// if no error , write message to kafka topic

	}