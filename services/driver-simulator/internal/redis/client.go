package redis
import (
	"context"
	"github.com/redis/go-redis/v9"
)
type Client struct{
	rdb *redis.Client
}

func New(addr string)*Client{
	rdb:=redis.NewClient(&redis.Options{
		Addr:addr,
	})
	return &Client{rdb:rdb};
}

func (c *Client) UpdateDriverLocation(
	ctx context.Context,
	driverID string,
	lat,lng float64,
)error {
	return c.rdb.GeoAdd(ctx,"drivers:locations",&redis.GeoLocation{
		Name:driverID,
		Longitude: lng,
		Latitude: lat,
	}).Err()	
}