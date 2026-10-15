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

	if err := app.Run(ctx); err != nil {
		stop()
		log.Fatal(err)
	}
}
