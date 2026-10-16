package server

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"ridepulse/pkg/health"
	"ridepulse/services/api-gateway/internal/handlers"
	"ridepulse/services/api-gateway/internal/metrics"
	"ridepulse/services/api-gateway/internal/ports"
)

// readyTimeout bounds the whole readiness probe, so a slow broker cannot make
// the probe itself hang past the orchestrator's timeout.
const readyTimeout = 1 * time.Second

// NewRouter builds the gateway routes. ready holds the dependency checks used
// by GET /ready.
func NewRouter(publisher ports.EventPublisher, ready map[string]health.Check) *gin.Engine {
	metrics.Register()

	r := gin.New()
	r.Use(gin.Recovery())
	// gin.Logger() is deliberately NOT installed here — under RPS benchmarks
	// its stdout writes become the bottleneck long before anything else does.
	registerRoutes(r, publisher, ready)
	return r
}

func registerRoutes(r *gin.Engine, publisher ports.EventPublisher, ready map[string]health.Check) {
	r.GET("/health", gin.WrapH(health.Liveness()))
	r.GET("/ready", gin.WrapH(health.Readiness(readyTimeout, ready)))
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	r.POST("/rides", handlers.CreateRide(publisher))
}
