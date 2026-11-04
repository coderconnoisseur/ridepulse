package redis

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"ridepulse/services/matching-service/internal/metrics"
)

type DriverRepository struct {
	client *redis.Client
}

// NewDriverRepository connects to addr with a pool of poolSize connections.
// Commands honour context deadlines, so a claim can't outlive its timeout.
func NewDriverRepository(addr string, poolSize int) *DriverRepository {
	rdb := redis.NewClient(&redis.Options{
		Addr:                  addr,
		PoolSize:              poolSize,
		ContextTimeoutEnabled: true,
	})
	return &DriverRepository{client: rdb}
}

// Redis keys shared with driver-simulator. Do not rename.
const (
	locationsKey      = "drivers:locations"
	assignedKeyPrefix = "driver:assigned:" // <driverID>, value = rideID (safety net TTL)

	assignedTTL   = 10 * time.Minute
	rideDriverTTL = time.Hour // ride:driver:<rideID>, value = driverID (idempotency record)

	// candidateCount is how many nearest drivers one claim examines. Assigned
	// drivers leave the GEO set, so the first candidate is nearly always free.
	candidateCount = 20
)

func rideDriverKey(rideID string) string { return "ride:driver:" + rideID }

// claimScript assigns the nearest free driver as one atomic step, so concurrent
// rides can never collide over a driver and need no locks.
//
// It builds driver:assigned keys at runtime, so it needs a single Redis
// instance, not Redis Cluster.
//
// KEYS: ride:driver, drivers:locations.
// ARGV: rideID, lng, lat, radius km, candidate count, assigned TTL s,
// ride:driver TTL s, assigned key prefix.
// Returns {driverID, replayed}, or false if no free driver is in range.
var claimScript = redis.NewScript(`
local existing = redis.call("GET", KEYS[1])
if existing then
	return {existing, 1}
end
local candidates = redis.call("GEORADIUS_RO", KEYS[2], ARGV[2], ARGV[3], ARGV[4], "km", "COUNT", ARGV[5], "ASC")
for _, driver in ipairs(candidates) do
	local assigned = ARGV[8] .. driver
	if redis.call("EXISTS", assigned) == 0 then
		redis.call("SET", assigned, ARGV[1], "EX", ARGV[6])
		redis.call("SET", KEYS[1], driver, "EX", ARGV[7])
		redis.call("ZREM", KEYS[2], driver)
		return {driver, 0}
	end
end
return false
`)

// ClaimNearestDriver implements ports.DriverRepository.
func (r *DriverRepository) ClaimNearestDriver(
	ctx context.Context,
	rideID string,
	lat, lng, radiusKM float64,
) (string, bool, error) {
	start := time.Now()
	res, err := claimScript.Run(
		ctx,
		r.client,
		[]string{rideDriverKey(rideID), locationsKey},
		rideID,
		lng,
		lat,
		radiusKM,
		candidateCount,
		int64(assignedTTL.Seconds()),
		int64(rideDriverTTL.Seconds()),
		assignedKeyPrefix,
	).Slice()
	metrics.DriverClaimLatency.Observe(time.Since(start).Seconds())
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	driverID, _ := res[0].(string)
	replayed, _ := res[1].(int64)
	return driverID, replayed == 1, nil
}

// Ping checks that Redis answers; used by the /ready probe.
func (r *DriverRepository) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// Close closes the underlying Redis client.
func (r *DriverRepository) Close() error {
	return r.client.Close()
}
