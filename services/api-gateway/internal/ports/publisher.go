package ports

import (
	"context"

	"ridepulse/pkg/events"
)

type EventPublisher interface {
	PublishRideRequested(ctx context.Context, event events.RideRequestedEvent) error
}
