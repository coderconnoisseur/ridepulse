//interfaces
package ports

import (
	"context"

	"ridepulse/services/pricing-service/internal/domain"
)

// RideRequestedConsumer delivers RideRequested events to a handler until ctx
// is cancelled. A nil error from the handler acknowledges the event; an error
// makes the consumer retry it and eventually dead-letter it.
type RideRequestedConsumer interface {
	Run(ctx context.Context, handler func(ctx context.Context, event domain.RideRequestedEvent) error) error
	Close() error
}
