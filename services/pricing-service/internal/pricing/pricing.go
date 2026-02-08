//pricing logic
package pricing

import (
	"math"
	"ridepulse/services/pricing-service/internal/domain"
)

func ComputePrice(event domain.RideRequestedEvent) domain.RidePricedEvent{
	baseFare:=50.0 //for mvp
	distanceKM:=math.Hypot(
		event.Pickup.Lat- event.Drop.Lat,
		event.Pickup.Lng-event.Drop.Lng,
	)*10 //scale factor
	price:=baseFare + (distanceKM *12) // 12 per km just till mvp develops
	return domain.RidePricedEvent{
		RideId:event.RideId,
		Price:price,
		Currency:"INR",
		SurgeMultiplier: 1.0,//till mvp
	}
}
