package handlers

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"ridepulse/pkg/events"
	"ridepulse/services/api-gateway/internal/metrics"
	"ridepulse/services/api-gateway/internal/ports"
)

// coordinate is the request-side point. Pointers let validation tell a missing
// field from a valid 0 value.
type coordinate struct {
	Lat *float64 `json:"lat"`
	Lng *float64 `json:"lng"`
}

type rideRequest struct {
	Pickup *coordinate `json:"pickup"`
	Drop   *coordinate `json:"drop"`
}

// validate returns the first problem with the request, or nil.
func (req rideRequest) validate() error {
	if err := validatePoint("pickup", req.Pickup); err != nil {
		return err
	}
	return validatePoint("drop", req.Drop)
}

func validatePoint(name string, p *coordinate) error {
	switch {
	case p == nil:
		return fmt.Errorf("%s is required", name)
	case p.Lat == nil:
		return fmt.Errorf("%s.lat is required", name)
	case p.Lng == nil:
		return fmt.Errorf("%s.lng is required", name)
	case *p.Lat < -90 || *p.Lat > 90:
		return fmt.Errorf("%s.lat must be between -90 and 90", name)
	case *p.Lng < -180 || *p.Lng > 180:
		return fmt.Errorf("%s.lng must be between -180 and 180", name)
	}
	return nil
}

func toLocation(p *coordinate) events.Location {
	return events.Location{Lat: *p.Lat, Lng: *p.Lng}
}

func CreateRide(publisher ports.EventPublisher) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		defer func() {
			metrics.RequestDuration.Observe(time.Since(start).Seconds())
		}()

		var req rideRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			metrics.BadRequests.Inc()
			c.JSON(400, gin.H{"error": "Invalid request payload"})
			return
		}
		if err := req.validate(); err != nil {
			metrics.BadRequests.Inc()
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		metrics.RequestsTotal.Inc()
		rideID := uuid.NewString()

		if err := publisher.PublishRideRequested(c.Request.Context(), events.RideRequestedEvent{
			Version:     events.Version,
			RideID:      rideID,
			Pickup:      toLocation(req.Pickup),
			Drop:        toLocation(req.Drop),
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
