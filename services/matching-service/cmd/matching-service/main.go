package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"ridepulse/services/matching-service/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := app.LoadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("matching-service: invalid configuration: %v", err)
	}
	log.Printf("matching-service config: metrics=%s brokers=%v redis=%s workers=%d redis_pool=%d deadline=%s", cfg.MetricsAddr, cfg.KafkaBrokers, cfg.RedisAddr, cfg.MatchWorkers, cfg.RedisPoolSize, cfg.MatchDeadline)

	if err := app.Run(ctx, cfg); err != nil {
		stop()
		log.Fatal(err)
	}
}
