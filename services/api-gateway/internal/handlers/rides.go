package handlers

import (
	"github.com/gin-gonic/gin"
	// "encoding/json"
	"ridepulse/services/api-gateway/internal/domain"
	"ridepulse/services/api-gateway/internal/ports"
	"time"
	"log"
	"github.com/google/uuid"
)

type Location struct {
	Lat float64 `json:"lat" binding:"required"`
	Lng float64 `json:"lng" binding:"required"`
}
type RideRequest struct {
	Pickup      Location `json:"pickup" binding:"required"`
	Drop     Location `json:"drop" binding:"required"`
}


func CreateRide(publisher ports.EventPublisher) gin.HandlerFunc {
	return func(c *gin.Context) {
		start:=time.Now()
		rideID := uuid.NewString()
		var req RideRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "Invalid request payload"})
			return
		}
		log.Println("1",time.Since(start))
		if err:=publisher.PublishRideRequested(c.Request.Context(),domain.RideRequestedEvent{
			RideID: rideID,
			Pickup: domain.Location{
				Lat: req.Pickup.Lat,
				Lng: req.Pickup.Lng,
			},
			Drop: domain.Location{
				Lat: req.Drop.Lat,
				Lng: req.Drop.Lng,
			},
		});err!=nil{
			c.JSON(503,gin.H{
				"error": "Failed to create ride.",
			})
			return
		}
		log.Println("2",time.Since(start))
		c.JSON(201,gin.H{
		"ride_id": rideID,
		"status": "REQUESTED",
		})
}}