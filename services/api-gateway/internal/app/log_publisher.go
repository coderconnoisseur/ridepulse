package app

import (
	"context"
	"log"
	"ridepulse/services/api-gateway/internal/domain"
)

type LogPublisher struct{}

func (p *LogPublisher) PublishRideRequested(ctx context.Context, event domain.RideRequestedEvent) error {
	//this function automatically implements the EventPublisher interface
	//coz it has the same method signature
	// and it takes LogPublisher as a receiver
	log.Printf("Published ride requested event: %+v\n", event) //logs messages to console
	return nil // nil= no error , not nil =error
}