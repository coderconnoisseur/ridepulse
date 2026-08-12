package httpapi

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"ridepulse/services/assignment-service/internal/store"
)

// Serve starts the metrics + assignment-query HTTP server. Blocks until the
// server exits. Endpoints:
//
//	GET /metrics                 — Prometheus scrape endpoint
//	GET /assignments/<rideID>    — look up a single assignment (JSON)
//	GET /count                   — total assignments stored (plaintext)
func Serve(addr string, mem *store.Memory) error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/assignments/", queryHandler(mem))
	mux.HandleFunc("/count", countHandler(mem))

	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}
	log.Printf("assignment-service listening on %s", addr)
	return server.ListenAndServe()
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

func countHandler(mem *store.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%d\n", mem.Count())
	}
}
