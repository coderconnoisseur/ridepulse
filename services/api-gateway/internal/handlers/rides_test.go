package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"ridepulse/pkg/events"
)

type fakePublisher struct {
	err       error
	published []events.RideRequestedEvent
}

func (f *fakePublisher) PublishRideRequested(_ context.Context, e events.RideRequestedEvent) error {
	f.published = append(f.published, e)
	return f.err
}

func post(t *testing.T, pub *fakePublisher, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/rides", CreateRide(pub))
	req := httptest.NewRequest(http.MethodPost, "/rides", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestCreateRideAccepted(t *testing.T) {
	pub := &fakePublisher{}
	rec := post(t, pub, `{"pickup":{"lat":12.9,"lng":77.6},"drop":{"lat":12.95,"lng":77.65}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if len(pub.published) != 1 {
		t.Fatalf("published %d events, want 1", len(pub.published))
	}
	e := pub.published[0]
	if e.Version != events.Version || e.RideID == "" || e.RequestedAt == 0 {
		t.Errorf("event metadata wrong: %+v", e)
	}
	if e.Pickup != (events.Location{Lat: 12.9, Lng: 77.6}) || e.Drop != (events.Location{Lat: 12.95, Lng: 77.65}) {
		t.Errorf("locations wrong: %+v", e)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["ride_id"] != e.RideID || body["status"] != "REQUESTED" {
		t.Errorf("response %v does not match event %s", body, e.RideID)
	}
}

func TestCreateRideAcceptsZeroCoordinates(t *testing.T) {
	pub := &fakePublisher{}
	rec := post(t, pub, `{"pickup":{"lat":0,"lng":0},"drop":{"lat":-90,"lng":180}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if got := pub.published[0].Pickup; got != (events.Location{}) {
		t.Errorf("pickup = %+v, want zero location", got)
	}
}

func TestCreateRideRejectsInvalidInput(t *testing.T) {
	cases := map[string]string{
		"missing pickup":     `{"drop":{"lat":1,"lng":1}}`,
		"null drop":          `{"pickup":{"lat":1,"lng":1},"drop":null}`,
		"missing pickup.lat": `{"pickup":{"lng":1},"drop":{"lat":1,"lng":1}}`,
		"missing drop.lng":   `{"pickup":{"lat":1,"lng":1},"drop":{"lat":1}}`,
		"lat above 90":       `{"pickup":{"lat":90.1,"lng":1},"drop":{"lat":1,"lng":1}}`,
		"lat below -90":      `{"pickup":{"lat":1,"lng":1},"drop":{"lat":-91,"lng":1}}`,
		"lng above 180":      `{"pickup":{"lat":1,"lng":180.5},"drop":{"lat":1,"lng":1}}`,
		"lng below -180":     `{"pickup":{"lat":1,"lng":1},"drop":{"lat":1,"lng":-181}}`,
		"not json":           `{"pickup":`,
		"string coordinate":  `{"pickup":{"lat":"1","lng":1},"drop":{"lat":1,"lng":1}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			pub := &fakePublisher{}
			rec := post(t, pub, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code=%d, want 400; body=%s", rec.Code, rec.Body)
			}
			if len(pub.published) != 0 {
				t.Fatalf("invalid request was published")
			}
		})
	}
}

func TestCreateRideBoundaryValuesAccepted(t *testing.T) {
	pub := &fakePublisher{}
	rec := post(t, pub, `{"pickup":{"lat":90,"lng":-180},"drop":{"lat":-90,"lng":180}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestCreateRidePublishFailureIs503(t *testing.T) {
	pub := &fakePublisher{err: errors.New("kafka down")}
	rec := post(t, pub, `{"pickup":{"lat":1,"lng":1},"drop":{"lat":1,"lng":1}}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d, want 503", rec.Code)
	}
}
