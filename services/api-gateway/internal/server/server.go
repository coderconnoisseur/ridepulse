package server

import (
	"ridepulse/services/api-gateway/internal/handlers"
	"ridepulse/services/api-gateway/internal/ports"
	"github.com/gin-gonic/gin"
)

func NewRouter(publisher ports.EventPublisher)*gin.Engine{
	r:=gin.New()
	r.Use(gin.Recovery())
	r.Use(gin.Logger())
	registerRoutes(r, publisher)
	return r
}



func registerRoutes(r *gin.Engine, publisher ports.EventPublisher){
	r.GET("/health", handlers.Health())

	r.POST("/rides",handlers.CreateRide(publisher))
}