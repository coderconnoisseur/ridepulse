package redis

import(
"context"
"fmt"
"time"
"github.com/redis/go-redis/v9"
)
type DriverRepository struct{
	client *redis.Client
}

func NewDriverRepository (addr string)*DriverRepository{
	rdb:=redis.NewClient(&redis.Options{
		Addr:addr,
	})
return &DriverRepository{client:rdb}
}

func (r *DriverRepository) FindNearbyDrivers(
	ctx context.Context,
	lat,lng float64,
	radiusKm float64,
)([]string ,error){
	res,err:=r.client.GeoRadius(
		ctx,
		"drivers:locations",
		lng,
		lat,
		&redis.GeoRadiusQuery{
			Radius: radiusKm,
			Unit: "km",
			Count:20,
			Sort: "ASC",
		},).Result()
	if err!=nil{
		return nil,err
	}
	var drivers[]string;
	for _ ,r :=range res{
		drivers=append(drivers,r.Name)
	}
		return drivers,nil
	}

func (r*DriverRepository) TryLockDriver(
	ctx context.Context,
	driverID,
	RideID string,
)(bool,error){
	
	key:=fmt.Sprintf("driver:lock:%s",driverID)
	ok,err:=r.client.SetNX(
		ctx,
		key,
		RideID,
		3*time.Second,	//time to live =3sec for this lock
	).Result()
	return ok,err
}