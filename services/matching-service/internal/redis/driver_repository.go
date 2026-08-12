package redis

import(
"context"
"fmt"
"time"
"github.com/redis/go-redis/v9"
"ridepulse/services/matching-service/internal/metrics"
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

// unlockScript releases a driver lock only if its current value matches the
// caller's rideID. Prevents deleting a lock that has already expired and been
// reacquired by a different ride between our GET and DEL.
var unlockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
else
	return 0
end
`)

func (r *DriverRepository) FindNearbyDrivers(
	ctx context.Context,
	lat,lng float64,
	radiusKm float64,
)([]string ,error){
	start:=time.Now()
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
	metrics.RedisGeoLatency.Observe(time.Since(start).Seconds())
	if err!=nil{
		return nil,err
	}
	var drivers[]string;
	for _ ,r :=range res{
		drivers=append(drivers,r.Name)
	}
	metrics.NearbyDriversGauge.Set(float64(len(drivers)))
		return drivers,nil
	}

func (r*DriverRepository) TryLockDriver(
	ctx context.Context,
	driverID,
	RideID string,
)(bool,error){
	start:=time.Now()
	key:=fmt.Sprintf("driver:lock:%s",driverID)
	ok,err:=r.client.SetNX(
		ctx,
		key,
		RideID,
		3*time.Second,	//time to live =3sec for this lock
	).Result()
	metrics.DriverLockLatency.Observe(time.Since(start).Seconds())
	if err!=nil{
		return false,err
	}
	if ok{
		metrics.DriverLockSuccess.Inc()
	}else{
		metrics.DriverLockConflict.Inc()
	}
	return ok,nil
}

// UnlockDriver releases a lock we own. Used to reclaim "phantom" locks:
// SETNX operations that succeeded but lost the batch race to another driver.
// The Lua script guarantees we never delete a lock that some other ride has
// since acquired (after TTL expiry or lock churn).
func (r *DriverRepository) UnlockDriver(
	ctx context.Context,
	driverID, rideID string,
) error {
	key := fmt.Sprintf("driver:lock:%s", driverID)
	_, err := unlockScript.Run(ctx, r.client, []string{key}, rideID).Result()
	if err == redis.Nil {
		return nil
	}
	return err
}