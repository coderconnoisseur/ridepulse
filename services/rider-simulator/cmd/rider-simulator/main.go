package main

import (
	"context"
	"log"
	"time"
	"ridepulse/services/rider-simulator/internal/simulator"
)
func main() {
	ctx := context.Background()
	s := simulator.New("http://localhost:8080/rides")
	log.Println("Starting rider simulator...")
	// example: 10 workers, each 10 req/sec → 100 RPS
	s.Run(ctx, 50, 100*time.Millisecond)

	select {}
}
