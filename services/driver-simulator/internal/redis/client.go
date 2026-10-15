package redis

import (
	"context"
	"errors"

	"github.com/redis/go-redis/v9"
)

const (
	locationsKey      = "drivers:locations"
	assignedKeyPrefix = "driver:assigned:"
)

// updateScript reports the driver's assigned ride and skips the GEOADD while
// the driver is busy; otherwise it records the location and returns nil.
// KEYS[1] = driver:assigned:<id>, KEYS[2] = drivers:locations
// ARGV = lng, lat, driverID
var updateScript = redis.NewScript(`
local ride = redis.call('GET', KEYS[1])
if ride then
	return ride
end
redis.call('GEOADD', KEYS[2], ARGV[1], ARGV[2], ARGV[3])
return false
`)

// releaseScript frees the driver only if it is still assigned to the given ride.
// KEYS[1] = driver:assigned:<id>, ARGV[1] = rideID
var releaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
	return redis.call('DEL', KEYS[1])
end
return 0
`)

type Client struct {
	rdb *redis.Client
}

func New(addr string) *Client {
	rdb := redis.NewClient(&redis.Options{
		Addr: addr,
	})
	return &Client{rdb: rdb}
}

func (c *Client) Close() error {
	return c.rdb.Close()
}

// UpdateDriverLocation publishes the driver's position unless the driver is
// assigned to a ride, in which case the position is left untouched and the
// assigned rideID is returned. An empty rideID means the location was written.
func (c *Client) UpdateDriverLocation(
	ctx context.Context,
	driverID string,
	lat, lng float64,
) (assignedRideID string, err error) {
	res, err := updateScript.Run(ctx, c.rdb,
		[]string{assignedKeyPrefix + driverID, locationsKey},
		lng, lat, driverID,
	).Text()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return res, err
}

// ReleaseDriver clears the driver's assignment if it still belongs to rideID.
func (c *Client) ReleaseDriver(ctx context.Context, driverID, rideID string) error {
	return releaseScript.Run(ctx, c.rdb,
		[]string{assignedKeyPrefix + driverID}, rideID,
	).Err()
}
