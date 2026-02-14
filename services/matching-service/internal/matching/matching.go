package matching
import(
	"errors"
	"time"
	"ridepulse/services/matching-service/internal/domain"
	"ridepulse/services/matching-service/internal/ports"
)
type Service struct{
	repo ports.DriverRepository
}

func New(repo ports.DriverRepository)*Service{
	return &Service{repo:repo}
}

func (s *Service) Match(event domain.RidePricedEvent)(domain.RideMatchedEvent, error){
	//pickup will be passed later
	drivers,err:=s.repo.FindNearbyDrivers(event.Pickup.Lat,event.Pickup.Lng,5)
	if err!=nil{
		return domain.RideMatchedEvent{}, err
	}
	for _,d :=range drivers{
		ok,err:=s.repo.TryLockDriver(d,event.RideID)
		if err!=nil{
			continue
		}
		if ok{
			return domain.RideMatchedEvent{
				RideID:event.RideID,
				DriverID:d,
				MatchedAT: time.Now().Unix(),
			}, nil
		}
	}
	return domain.RideMatchedEvent{},errors.New("no drivers available")
}