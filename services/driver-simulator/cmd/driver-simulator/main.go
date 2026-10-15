package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"ridepulse/services/driver-simulator/internal/redis"
	"ridepulse/services/driver-simulator/internal/simulator"
)

func main() {
	driverCount := 1000
	if v := os.Getenv("DRIVER_COUNT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			log.Fatalf("invalid DRIVER_COUNT %q: must be a positive integer", v)
		}
		driverCount = n
	}

	tripDuration := 5 * time.Second
	if v := os.Getenv("DRIVER_TRIP_DURATION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			log.Fatalf("invalid DRIVER_TRIP_DURATION %q: must be a positive Go duration such as 5s", v)
		}
		tripDuration = d
	}

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	r := redis.New(redisAddr)
	sim := simulator.New(driverCount, tripDuration)

	log.Printf("starting driver simulator: drivers=%d trip=%s redis=%s", driverCount, tripDuration, redisAddr)
	sim.Run(ctx, r)

	if err := r.Close(); err != nil {
		log.Printf("closing redis: %v", err)
	}
	log.Println("driver simulator stopped")
}
