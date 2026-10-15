package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"ridepulse/services/assignment-service/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := app.LoadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("assignment-service: invalid configuration: %v", err)
	}
	log.Printf("assignment-service config: http=%s brokers=%v", cfg.HTTPAddr, cfg.KafkaBrokers)

	if err := app.Run(ctx, cfg); err != nil {
		stop()
		log.Fatal(err)
	}
}
