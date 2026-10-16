package app

import (
	"github.com/gin-gonic/gin"

	"ridepulse/pkg/health"
	"ridepulse/services/api-gateway/internal/server"
)

// NewApp wires the router and returns it with a close func that releases the
// Kafka publisher; call it after the HTTP server has shut down.
func NewApp(cfg Config) (*gin.Engine, func() error) {
	publisher := NewKafkaPublisher(cfg.KafkaBrokers)
	ready := map[string]health.Check{
		"kafka": health.Kafka(cfg.KafkaBrokers),
	}
	return server.NewRouter(publisher, ready), publisher.Writer.Close
}
