package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ridepulse/services/dashboard/internal/dashboard"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := dashboard.LoadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("dashboard: invalid configuration: %v", err)
	}

	state := dashboard.NewState()
	load := dashboard.NewLoad(cfg.GatewayURL, state, cfg.InitialDemand)
	collector := dashboard.NewCollector(cfg, state, load)
	defer collector.Close()
	crasher := dashboard.NewCrasher(cfg.DockerSocket, cfg.CrashTarget)

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           dashboard.NewServer(collector, load, state, crasher).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go collector.Run(ctx)
	go load.Run(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()

	log.Printf("dashboard listening on %s", cfg.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("dashboard: %v", err)
	}
}
