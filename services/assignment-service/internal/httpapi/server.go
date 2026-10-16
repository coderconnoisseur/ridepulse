package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"ridepulse/pkg/health"
	"ridepulse/services/assignment-service/internal/domain"
	"ridepulse/services/assignment-service/internal/store"
)

// readyTimeout bounds the whole /ready probe.
const readyTimeout = 1 * time.Second

// NewServer builds the metrics + assignment-query HTTP server. The caller
// starts it with ListenAndServe and stops it with Shutdown. ready holds the
// dependency checks used by GET /ready. Endpoints:
//
//	GET /health                  — liveness (always 200 while running)
//	GET /ready                   — readiness (200 when dependencies answer)
//	GET /metrics                 — Prometheus scrape endpoint
//	GET /assignments/<rideID>    — look up a single assignment (JSON)
//	GET /rides/<rideID>          — matched/unmatched outcome of a ride (JSON)
//	GET /count                   — total matched assignments stored (plaintext)
func NewServer(addr string, mem *store.Memory, ready map[string]health.Check) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/health", health.Liveness())
	mux.Handle("/ready", health.Readiness(readyTimeout, ready))
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/assignments/", queryHandler(mem))
	mux.HandleFunc("/rides/", rideHandler(mem))
	mux.HandleFunc("/count", countHandler(mem))

	return &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
}

func queryHandler(mem *store.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rideID := strings.TrimPrefix(r.URL.Path, "/assignments/")
		if rideID == "" {
			http.Error(w, "missing ride id", http.StatusBadRequest)
			return
		}
		a, ok := mem.Get(rideID)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a)
	}
}

type matchedBody struct {
	RideID    string `json:"ride_id"`
	Status    string `json:"status"`
	DriverID  string `json:"driver_id"`
	MatchedAT int64  `json:"matched_at"`
	LatencyMs int64  `json:"latency_ms"`
}

type unmatchedBody struct {
	RideID   string `json:"ride_id"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
	FailedAt int64  `json:"failed_at"`
}

func rideHandler(mem *store.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rideID := strings.TrimPrefix(r.URL.Path, "/rides/")
		if rideID == "" {
			http.Error(w, "missing ride id", http.StatusBadRequest)
			return
		}
		o, ok := mem.Status(rideID)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var body any
		if o.Status == domain.StatusMatched {
			body = matchedBody{
				RideID:    o.RideID,
				Status:    o.Status,
				DriverID:  o.Assignment.DriverID,
				MatchedAT: o.Assignment.MatchedAT,
				LatencyMs: o.Assignment.LatencyMs,
			}
		} else {
			body = unmatchedBody{RideID: o.RideID, Status: o.Status, Reason: o.Reason, FailedAt: o.FailedAt}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}
}

func countHandler(mem *store.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%d\n", mem.Count())
	}
}
