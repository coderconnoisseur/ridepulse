package simulator

import (
	"fmt"
	"math/rand/v2"
	"time"
	"context"
	// "github.com/redis/go-redis/v9"
	"ridepulse/services/driver-simulator/internal/redis"
)
type Driver struct{
	ID string
	Lat float64
	Lng float64
}
type Simulator struct{
	drivers []Driver
}
func New(numDrivers int )*Simulator{
	drivers:=make([]Driver,numDrivers)
	
	for i:=0;i<numDrivers;i++{
		drivers[i]=Driver{
			ID:fmt.Sprintf("driver-%d",i),
			Lat:12.9 + rand.Float64()*0.02,
			Lng:77.6 + rand.Float64()*0.02,
		}
	}
	return &Simulator{drivers:drivers}
}
func (d *Driver)Move(){
	d.Lat += (rand.Float64()-0.5)*0.001
	d.Lng += (rand.Float64()-0.5)*0.001
}

func (s *Simulator)Run(ctx context.Context,redisClient *redis.Client){
	for i:=range(s.drivers){
		driver:=&s.drivers[i]
		go func (d *Driver){
			ticker:=time.NewTicker(500*time.Millisecond)
			defer ticker.Stop()
				for{
					select{
					case<-ticker.C:
						d.Move()
						redisClient.UpdateDriverLocation(
							ctx,
							d.ID,
							d.Lat,
							d.Lng,
						)
						case<-ctx.Done():
							return
					}
				}
		}(driver)
	}
}