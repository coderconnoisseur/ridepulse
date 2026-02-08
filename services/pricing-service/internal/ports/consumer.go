//interfaces
package ports

//lets import models for event
import "ridepulse/services/pricing-service/internal/domain"
//Consumer interface for consuming messages

type RideRequestedConsumer interface {
	// any type that has following func is a RideRequestedConsumer
	ConsumeRideRequested(handler func(event domain.RideRequestedEvent) error) error
	//will call handler func whenever a RideRequestedEvent is consumed
	// i dont manage the loop here , kafka does , i just call handler with event
}
