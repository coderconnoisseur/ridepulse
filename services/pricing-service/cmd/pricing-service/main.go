// process entrypoint
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"ridepulse/services/pricing-service/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := app.LoadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("pricing-service: invalid configuration: %v", err)
	}
	log.Printf("pricing-service config: metrics=%s brokers=%v", cfg.MetricsAddr, cfg.KafkaBrokers)

	if err := app.Run(ctx, cfg); err != nil {
		stop()
		log.Fatal(err)
	}
}
