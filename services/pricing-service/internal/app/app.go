//dependency wiring
package app
import(
	"context"
	// "log"
	"ridepulse/services/pricing-service/internal/domain"
	"ridepulse/services/pricing-service/internal/kafka"
	"ridepulse/services/pricing-service/internal/pricing"
	// "ridepulse/services/pricing-service/internal/ports"
)

func Run()error{
	ctx:=context.Background()//for a graceful shutdown

	consumer:=kafka.NewKafkaConsumer(	//creation of NewKafkaConsumer  
		[]string{"localhost:9092"},		//broker running at localhost:9092,as of now single broker , might scale in future
		"pricing-service",			//group id for consumer group , if i scale this service to multiple instances they will share the same group id and kafka will load balance messages between them
		"ride.requested",			//name of topic to consume from 
	)
	publisher:=kafka.NewKafkaPublisher([]string{"localhost:9092"})
	pool:=NewWorkerPool(100)	//creating a worker pool with buffersize 100(my buffer will handle 100 events before blocking , after that backpressure will kick)
	pool.Start(ctx,func(event domain.RideRequestedEvent)error{
		// log.Printf("Recieved Event: %+v",event)//for now just log the event , later ill implement pricing logic here
		priced:=pricing.ComputePrice(event)
		return publisher.PublishRidePriced(ctx,priced)
	})
	return consumer.ConsumeRideRequested(func(event domain.RideRequestedEvent)error{
		pool.Enqueue(event)
		return nil
	})
}