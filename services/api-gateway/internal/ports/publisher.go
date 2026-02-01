package ports

import (
	"context"
	"ridepulse/services/api-gateway/internal/domain"
)

type EventPublisher interface {
	PublishRideRequested(ctx context.Context, event domain.RideRequestedEvent) error
}
