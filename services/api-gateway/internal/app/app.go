package app

import (
	"ridepulse/services/api-gateway/internal/server"

	"github.com/gin-gonic/gin"
)
func NewApp() *gin.Engine{
	publisher:=NewKafkaPublisher([]string {"localhost:9092"})
	return server.NewRouter(publisher)
}	