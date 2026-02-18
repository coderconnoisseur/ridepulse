package matching
import(
	"errors"
	"log"
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
log.Printf(
    "Matching ride %s at pickup: lat=%f lng=%f",
    event.RideID,
    event.Pickup.Lat,
    event.Pickup.Lng,
)

	drivers,err:=s.repo.FindNearbyDrivers(event.Pickup.Lat,event.Pickup.Lng,50)
	if err!=nil{
		return domain.RideMatchedEvent{}, err
	}
	
	log.Printf("Found %d nearby drivers", len(drivers))

	for _,d :=range drivers{
		log.Printf("Trying driver: %s", d)
		ok,err:=s.repo.TryLockDriver(d,event.RideID)
		if err!=nil{
			continue
		}
		if ok{
			log.Printf("Locked driver: %s", d)
			return domain.RideMatchedEvent{
				RideID:event.RideID,
				DriverID:d,
				MatchedAT: time.Now().Unix(),
			}, nil
		}
	}
	return domain.RideMatchedEvent{},errors.New("no drivers available")
}