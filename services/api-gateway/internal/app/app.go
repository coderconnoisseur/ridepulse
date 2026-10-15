package app

import (
	"ridepulse/services/api-gateway/internal/server"

	"github.com/gin-gonic/gin"
)

// NewApp wires the router and returns it with a close func that releases the
// Kafka publisher; call it after the HTTP server has shut down.
func NewApp(cfg Config) (*gin.Engine, func() error) {
	publisher := NewKafkaPublisher(cfg.KafkaBrokers)
	return server.NewRouter(publisher), publisher.Writer.Close
}
