package handlers

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"ridepulse/services/api-gateway/internal/domain"
	"ridepulse/services/api-gateway/internal/metrics"
	"ridepulse/services/api-gateway/internal/ports"
)

type Location struct {
	Lat float64 `json:"lat" binding:"required"`
	Lng float64 `json:"lng" binding:"required"`
}
type RideRequest struct {
	Pickup Location `json:"pickup" binding:"required"`
	Drop   Location `json:"drop" binding:"required"`
}

func CreateRide(publisher ports.EventPublisher) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		defer func() {
			metrics.RequestDuration.Observe(time.Since(start).Seconds())
		}()

		var req RideRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			metrics.BadRequests.Inc()
			c.JSON(400, gin.H{"error": "Invalid request payload"})
			return
		}

		metrics.RequestsTotal.Inc()
		rideID := uuid.NewString()

		if err := publisher.PublishRideRequested(c.Request.Context(), domain.RideRequestedEvent{
			RideID: rideID,
			Pickup: domain.Location{
				Lat: req.Pickup.Lat,
				Lng: req.Pickup.Lng,
			},
			Drop: domain.Location{
				Lat: req.Drop.Lat,
				Lng: req.Drop.Lng,
			},
			RequestedAt: time.Now().UnixNano(),
		}); err != nil {
			metrics.PublishErrors.Inc()
			c.JSON(503, gin.H{"error": "Failed to create ride."})
			return
		}

		c.JSON(201, gin.H{
			"ride_id": rideID,
			"status":  "REQUESTED",
		})
	}
}
