package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"ridepulse/services/matching-service/internal/metrics"
	"ridepulse/services/matching-service/internal/ports"
)

type DriverRepository struct {
	client *redis.Client
}

// NewDriverRepository connects to addr with a pool of poolSize connections.
func NewDriverRepository(addr string, poolSize int) *DriverRepository {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		PoolSize: poolSize,
	})
	return &DriverRepository{client: rdb}
}

// Redis keys shared with driver-simulator. Do not rename.
const (
	locationsKey = "drivers:locations"

	lockTTL       = 3 * time.Second  // driver:lock:<driverID>, value = rideID
	assignedTTL   = 10 * time.Minute // driver:assigned:<driverID>, value = rideID (safety net)
	rideDriverTTL = time.Hour        // ride:driver:<rideID>, value = driverID (idempotency record)
)

func lockKey(driverID string) string     { return fmt.Sprintf("driver:lock:%s", driverID) }
func assignedKey(driverID string) string { return fmt.Sprintf("driver:assigned:%s", driverID) }
func rideDriverKey(rideID string) string { return fmt.Sprintf("ride:driver:%s", rideID) }

// tryLockScript refuses assigned drivers, otherwise SET NX PX the lock.
// KEYS: assigned, lock. ARGV: rideID, lock TTL ms. Returns 1 if locked.
var tryLockScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 1 then
	return 0
end
if redis.call("SET", KEYS[2], ARGV[1], "NX", "PX", ARGV[2]) then
	return 1
end
return 0
`)

// assignScript atomically turns our lock into an assignment.
// KEYS: lock, ride:driver, assigned, drivers:locations.
// ARGV: rideID, driverID, assigned TTL s, ride:driver TTL s.
// Returns the assigned driverID, or false (nil reply) if we no longer hold
// the lock. If the ride already has a driver (retry/redelivery), our lock is
// dropped and the existing driver is returned untouched.
var assignScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) ~= ARGV[1] then
	return false
end
local existing = redis.call("GET", KEYS[2])
if existing then
	redis.call("DEL", KEYS[1])
	return existing
end
redis.call("SET", KEYS[3], ARGV[1], "EX", ARGV[3])
redis.call("SET", KEYS[2], ARGV[2], "EX", ARGV[4])
redis.call("ZREM", KEYS[4], ARGV[2])
redis.call("DEL", KEYS[1])
return ARGV[2]
`)

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
	lat, lng float64,
	radiusKm float64,
) ([]string, error) {
	start := time.Now()
	res, err := r.client.GeoRadius(
		ctx,
		locationsKey,
		lng,
		lat,
		&redis.GeoRadiusQuery{
			Radius: radiusKm,
			Unit:   "km",
			Count:  20,
			Sort:   "ASC",
		}).Result()
	metrics.RedisGeoLatency.Observe(time.Since(start).Seconds())
	if err != nil {
		return nil, err
	}
	var drivers []string
	for _, r := range res {
		drivers = append(drivers, r.Name)
	}
	metrics.NearbyDriversGauge.Set(float64(len(drivers)))
	return drivers, nil
}

// TryLockDriver takes the short-lived matching lock on a driver, refusing
// drivers that are already assigned to a ride.
func (r *DriverRepository) TryLockDriver(
	ctx context.Context,
	driverID,
	rideID string,
) (bool, error) {
	start := time.Now()
	res, err := tryLockScript.Run(
		ctx,
		r.client,
		[]string{assignedKey(driverID), lockKey(driverID)},
		rideID,
		lockTTL.Milliseconds(),
	).Int()
	metrics.DriverLockLatency.Observe(time.Since(start).Seconds())
	if err != nil {
		return false, err
	}
	ok := res == 1
	if ok {
		metrics.DriverLockSuccess.Inc()
	} else {
		metrics.DriverLockConflict.Inc()
	}
	return ok, nil
}

// UnlockDriver releases a lock we own. Used to reclaim "phantom" locks:
// SETNX operations that succeeded but lost the batch race to another driver.
// The Lua script guarantees we never delete a lock that some other ride has
// since acquired (after TTL expiry or lock churn).
func (r *DriverRepository) UnlockDriver(
	ctx context.Context,
	driverID, rideID string,
) error {
	_, err := unlockScript.Run(ctx, r.client, []string{lockKey(driverID)}, rideID).Result()
	if err == redis.Nil {
		return nil
	}
	return err
}

// Ping checks that Redis answers; used by the /ready probe.
func (r *DriverRepository) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// Close closes the underlying Redis client.
func (r *DriverRepository) Close() error {
	return r.client.Close()
}

// AssignDriver marks driverID as assigned to rideID, records the ride's
// driver for idempotent replay, removes the driver from the GEO set and
// releases the matching lock — all in one atomic script. Returns
// ports.ErrLockLost if our lock expired or was taken by another ride.
func (r *DriverRepository) AssignDriver(ctx context.Context, driverID, rideID string) (string, error) {
	assigned, err := assignScript.Run(
		ctx,
		r.client,
		[]string{lockKey(driverID), rideDriverKey(rideID), assignedKey(driverID), locationsKey},
		rideID,
		driverID,
		int64(assignedTTL.Seconds()),
		int64(rideDriverTTL.Seconds()),
	).Text()
	if err == redis.Nil {
		return "", ports.ErrLockLost
	}
	if err != nil {
		return "", err
	}
	return assigned, nil
}

// GetRideDriver returns the driver already assigned to rideID, if any.
func (r *DriverRepository) GetRideDriver(ctx context.Context, rideID string) (string, bool, error) {
	driverID, err := r.client.Get(ctx, rideDriverKey(rideID)).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return driverID, true, nil
}
