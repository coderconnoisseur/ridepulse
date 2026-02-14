package ports
import "ridepulse/services/matching-service/internal/domain"

type RidePricedConsumer interface{
	ConsumeRidePricedEvent(handler func(event domain.RidePricedEvent) error) error
}