package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"ridepulse/services/api-gateway/internal/app"
)

func main() {
	cfg, err := app.LoadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("api-gateway: invalid configuration: %v", err)
	}
	log.Printf("api-gateway config: http=%s brokers=%v", cfg.HTTPAddr, cfg.KafkaBrokers)

	r, closePublisher := app.NewApp(cfg)

	serv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: r,
	}
	go func() { // start server in a goroutine
		if err := serv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			panic(err)
		}
	}()
	// Block until an interrupt arrives, then shut down gracefully.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serv.Shutdown(ctx)
	// In-flight handlers have drained; flush and close the Kafka writer.
	if err := closePublisher(); err != nil {
		log.Printf("api-gateway publisher close: %v", err)
	}
}
