package kafka

import(
	"context"
	"encoding/json"
	// "errors"
	"github.com/segmentio/kafka-go"
	"ridepulse/services/pricing-service/internal/domain"
)
type KafkaConsumer struct{
	reader *kafka.Reader;
	
}

func NewKafkaConsumer(brokers [] string ,groupId,topic string)*KafkaConsumer{//will create new kafka consumer
	reader:=kafka.NewReader(kafka.ReaderConfig{
		Brokers:brokers,
		GroupID:groupId,
		Topic:topic,
		MinBytes: 1,
		MaxBytes: 10e6,
	})
	return &KafkaConsumer{
		reader:reader,
	}
}
func (c * KafkaConsumer)ConsumeRideRequested(
	handler func( event domain.RideRequestedEvent) error,
)error{
	for{
		msg,err:=c.reader.FetchMessage(context.Background())// i could have used ReadMessage too , but this gives me more control over committing offset
		if err!=nil{
			return err
		}//kafka read success till here , now ill unmarshal 

		var event domain.RideRequestedEvent;//ill store unmarshalled data here
		if err:=json.Unmarshal(msg.Value, &event);//unencoding the msg val and stores it in event , might return error
		err!=nil{
			//if there's an error in unmarshalling ill skip it and just commit offset (move to next message), no retries
			_=c.reader.CommitMessages(context.Background(),msg)
			continue
		}
		//unmarshalled successfully
		if err:=handler(event); err!=nil{// if handler returns error ,ill retry
			//do not commit , let kafka retry for same offset
			continue
		}
		//handler success , handler read that event successfully , lets commit offset 
		if err:=c.reader.CommitMessages(context.Background(),msg);err!=nil{
			return err// maybe ill make idempotent handler in future , but for now 
			//control flow is just retry on handler error and commit on success , if commit fails then return error and stop consuming
		}

	}
}