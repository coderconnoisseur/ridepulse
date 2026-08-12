package server

import (
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"ridepulse/services/api-gateway/internal/handlers"
	"ridepulse/services/api-gateway/internal/metrics"
	"ridepulse/services/api-gateway/internal/ports"
)

func NewRouter(publisher ports.EventPublisher) *gin.Engine {
	metrics.Register()

	r := gin.New()
	r.Use(gin.Recovery())
	// gin.Logger() is deliberately NOT installed here — under RPS benchmarks
	// its stdout writes become the bottleneck long before anything else does.
	registerRoutes(r, publisher)
	return r
}

func registerRoutes(r *gin.Engine, publisher ports.EventPublisher) {
	r.GET("/health", handlers.Health())
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	r.POST("/rides", handlers.CreateRide(publisher))
}
