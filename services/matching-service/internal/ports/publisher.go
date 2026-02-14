package ports
import (
	"context"
	"ridepulse/services/matching-service/internal/domain"
)
type RidePricedPublisher interface{
	PublishRidePricedEvent(ctx context.Context , event domain.RidePricedEvent) error
}