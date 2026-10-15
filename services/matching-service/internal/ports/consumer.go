package ports

import (
	"context"

	"ridepulse/services/matching-service/internal/domain"
)

// RidePricedConsumer delivers RidePriced events to a handler until ctx is
// cancelled. A nil error from the handler acknowledges the event; an error
// makes the consumer retry it and eventually dead-letter it.
type RidePricedConsumer interface {
	Run(ctx context.Context, handler func(ctx context.Context, event domain.RidePricedEvent) error) error
	Close() error
}
