//interfaces
package ports
import (
	"context"
	"ridepulse/services/pricing-service/internal/domain"
)

type RidePricedPublisher interface {
	PublishRidePriced(ctx context.Context, event domain.RidePricedEvent) error
	//context is passed for timeout , second arg is event that i'll publish
}