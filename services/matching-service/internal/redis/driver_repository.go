package redis

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"ridepulse/services/matching-service/internal/metrics"
	"ridepulse/services/matching-service/internal/ports"
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

	assignedTTL = 10 * time.Minute

	// matchedOutcomeTTL and unmatchedOutcomeTTL are how long a ride's outcome
	// (ride:outcome:<rideID>) is remembered for redeliveries. Unmatched rides
	// can arrive far faster than matched ones, so they get a shorter TTL to
	// keep Redis memory bounded under overload.
	matchedOutcomeTTL   = time.Hour
	unmatchedOutcomeTTL = 10 * time.Minute

	// candidateCount is how many nearest drivers one claim examines. Assigned
	// drivers leave the GEO set, so the first candidate is nearly always free.
	candidateCount = 20
)

func rideOutcomeKey(rideID string) string { return "ride:outcome:" + rideID }

// claimScript decides a ride's outcome once, as one atomic step: the nearest
// free driver, or no driver if none is free. Concurrent rides can't collide over
// a driver, and a redelivered ride gets the same outcome rather than a new one.
//
// It builds driver:assigned keys at runtime, so it needs a single Redis
// instance, not Redis Cluster.
//
// KEYS: ride:outcome, drivers:locations.
// ARGV: rideID, lng, lat, radius km, candidate count, assigned TTL s,
// matched outcome TTL s, assigned key prefix, now (Unix s), unmatched outcome TTL s.
// Returns {driverID, replayed, claimedAt}, or false if the ride has no driver.
var claimScript = redis.NewScript(`
local driver = redis.call("HGET", KEYS[1], "driver")
if driver then
	return {driver, 1, redis.call("HGET", KEYS[1], "claimed_at")}
end
if redis.call("EXISTS", KEYS[1]) == 1 then
	return false
end
local candidates = redis.call("GEORADIUS_RO", KEYS[2], ARGV[2], ARGV[3], ARGV[4], "km", "COUNT", ARGV[5], "ASC")
for _, candidate in ipairs(candidates) do
	local assigned = ARGV[8] .. candidate
	if redis.call("EXISTS", assigned) == 0 then
		redis.call("SET", assigned, ARGV[1], "EX", ARGV[6])
		redis.call("HSET", KEYS[1], "driver", candidate, "claimed_at", ARGV[9])
		redis.call("EXPIRE", KEYS[1], ARGV[7])
		redis.call("ZREM", KEYS[2], candidate)
		return {candidate, 0, ARGV[9]}
	end
end
redis.call("HSET", KEYS[1], "unmatched_at", ARGV[9])
redis.call("EXPIRE", KEYS[1], ARGV[10])
return false
`)

// ClaimNearestDriver implements ports.DriverRepository.
func (r *DriverRepository) ClaimNearestDriver(
	ctx context.Context,
	rideID string,
	lat, lng, radiusKM float64,
) (ports.Claim, error) {
	start := time.Now()
	res, err := claimScript.Run(
		ctx,
		r.client,
		[]string{rideOutcomeKey(rideID), locationsKey},
		rideID,
		lng,
		lat,
		radiusKM,
		candidateCount,
		int64(assignedTTL.Seconds()),
		int64(matchedOutcomeTTL.Seconds()),
		assignedKeyPrefix,
		start.Unix(),
		int64(unmatchedOutcomeTTL.Seconds()),
	).Slice()
	metrics.DriverClaimLatency.Observe(time.Since(start).Seconds())
	if err == redis.Nil {
		return ports.Claim{}, nil
	}
	if err != nil {
		return ports.Claim{}, err
	}
	driverID, _ := res[0].(string)
	replayed, _ := res[1].(int64)
	claimedAtRaw, _ := res[2].(string)
	claimedAt, err := strconv.ParseInt(claimedAtRaw, 10, 64)
	if err != nil {
		return ports.Claim{}, fmt.Errorf("claim time %q for ride %s: %w", claimedAtRaw, rideID, err)
	}
	return ports.Claim{DriverID: driverID, ClaimedAt: claimedAt, Replayed: replayed == 1}, nil
}

// Ping checks that Redis answers; used by the /ready probe.
func (r *DriverRepository) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// Close closes the underlying Redis client.
func (r *DriverRepository) Close() error {
	return r.client.Close()
}
