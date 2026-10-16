package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"ridepulse/pkg/health"
	"ridepulse/services/assignment-service/internal/store"
)

func TestHealthAndReadyRoutes(t *testing.T) {
	ready := map[string]health.Check{
		"kafka": func(context.Context) error { return errors.New("broker unreachable") },
	}
	srv := NewServer("", store.NewMemory(), ready)

	cases := map[string]int{
		"/health": http.StatusOK,
		"/ready":  http.StatusServiceUnavailable,
	}
	for path, want := range cases {
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s: code=%d, want %d", path, rec.Code, want)
		}
	}
}
