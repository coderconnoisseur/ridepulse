package kafka
import(
	"context"
	"encoding/json"
	"github.com/segmentio/kafka-go"
	"ridepulse/services/pricing-service/internal/domain"
)
type KafkaPublisher struct{
	writer *kafka.Writer;
}
func NewKafkaPublisher (brokers[] string)*KafkaPublisher{
	writer:=&kafka.Writer{
		Addr: kafka.TCP(brokers...),
		Topic:"ride.priced",
		Balancer: &kafka.Hash{},
		RequiredAcks: kafka.RequireOne,
		BatchTimeout: 10_000_000,//10ms
	}
	return &KafkaPublisher{writer}
}

func (p *KafkaPublisher) PublishRidePriced(
	ctx context.Context,
	event domain.RidePricedEvent,
)error{
	val,err:=json.Marshal(event)
	if err!=nil{
		return err
	}
	msg:=kafka.Message{
		Key: []byte(event.RideId),//kafka only understands byte arrays , (language agnostic)
		Value: val,
	}
	return p.writer.WriteMessages(ctx, msg)
}