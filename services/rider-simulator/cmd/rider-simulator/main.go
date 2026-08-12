package main

import (
	"context"
	"log"
	"os"
	"strconv"

	"ridepulse/services/rider-simulator/internal/simulator"
)

// Configuration comes from environment variables so the tools/benchmark
// runner can drive different ladder steps without editing source.
// Falls back to the previous hardcoded defaults so a plain
// `go run cmd/rider-simulator/main.go` still works.
func main() {
	ctx := context.Background()
	url := envDefault("RIDER_URL", "http://localhost:8080/rides")
	rps := envInt("RIDER_RPS", 500)
	concurrency := envInt("RIDER_CONCURRENCY", 200)

	s := simulator.New(url)
	log.Printf("Starting rider simulator: url=%s rps=%d concurrency=%d", url, rps, concurrency)
	s.Run(ctx, rps, concurrency)
	select {}
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
