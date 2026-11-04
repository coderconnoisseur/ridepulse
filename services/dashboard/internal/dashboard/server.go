package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

//go:embed web
var webFiles embed.FS

const (
	controlHeader = "X-RidePulse-Demo"
	crashDowntime = 2 * time.Second
	startAttempts = 5
)

// Controls are the demo's buttons. An interface lets tests run without Kafka or
// Docker.
type Controls interface {
	BadMessage(ctx context.Context) error
	Redeliver(ctx context.Context) (rideID, driverID string, ok bool, err error)
	Subscribe() (<-chan []byte, func())
	Recording() []byte
}

// Server serves the page, the frame stream and the demo controls.
type Server struct {
	controls Controls
	load     *Load
	state    *State
	crasher  *Crasher
	crashing atomic.Bool
}

// NewServer builds the server. crasher may be nil.
func NewServer(controls Controls, load *Load, state *State, crasher *Crasher) *Server {
	return &Server{controls: controls, load: load, state: state, crasher: crasher}
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	web, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(web))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("GET /api/config", s.config)
	mux.HandleFunc("GET /api/stream", s.stream)
	mux.HandleFunc("GET /api/recording", s.recording)
	mux.HandleFunc("POST /api/demand", s.control(s.demand))
	mux.HandleFunc("POST /api/rush", s.control(s.rush))
	mux.HandleFunc("POST /api/bad-message", s.control(s.badMessage))
	mux.HandleFunc("POST /api/redeliver", s.control(s.redeliver))
	mux.HandleFunc("POST /api/crash", s.control(s.crash))
	return mux
}

// control requires a custom header, which browsers won't let another website
// send without permission, so a page elsewhere can't press these buttons.
func (s *Server) control(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(controlHeader) == "" {
			http.Error(w, "missing "+controlHeader+" header", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	writeJSON(w, map[string]any{
		"max_demand": MaxDemand,
		"demand":     s.load.Demand(),
		"can_crash":  s.crasher != nil && s.crasher.Available(ctx),
	})
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	frames, stop := s.controls.Subscribe()
	defer stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case raw := <-frames:
			if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) recording(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(s.controls.Recording())
}

func (s *Server) demand(w http.ResponseWriter, r *http.Request) {
	rps, err := strconv.Atoi(r.URL.Query().Get("rps"))
	if err != nil {
		http.Error(w, "rps must be a number", http.StatusBadRequest)
		return
	}
	s.load.SetDemand(rps)
	writeJSON(w, map[string]int{"demand": s.load.Demand()})
}

func (s *Server) rush(w http.ResponseWriter, _ *http.Request) {
	s.load.StartRush(time.Now())
	s.state.Notice(fmt.Sprintf("Rush hour: %d requests per second for %d seconds", RushDemand, int(rushDuration.Seconds())))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) badMessage(w http.ResponseWriter, r *http.Request) {
	if err := s.controls.BadMessage(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.state.Notice("Sent an unreadable message to ride.requested")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) redeliver(w http.ResponseWriter, r *http.Request) {
	rideID, driverID, ok, err := s.controls.Redeliver(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if !ok {
		http.Error(w, "no ride has been matched yet", http.StatusConflict)
		return
	}
	s.state.Notice(fmt.Sprintf("Delivered ride %s again. It should keep %s, and replays and duplicates should each rise by one", shortID(rideID), driverID))
	w.WriteHeader(http.StatusNoContent)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// crash force-kills matching and starts it again shortly after. It isn't tied to
// the request, so closing the page can't leave matching down.
func (s *Server) crash(w http.ResponseWriter, r *http.Request) {
	if s.crasher == nil {
		http.Error(w, "crash demo not available", http.StatusNotFound)
		return
	}
	if !s.crashing.CompareAndSwap(false, true) {
		http.Error(w, "already crashing", http.StatusConflict)
		return
	}
	ctx := context.WithoutCancel(r.Context())
	if err := s.crasher.Kill(ctx); err != nil {
		s.crashing.Store(false)
		log.Printf("dashboard: crash: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.state.Notice("Matching service force-killed. Rides now wait in its Kafka queue")
	go func() {
		defer s.crashing.Store(false)
		time.Sleep(crashDowntime)
		for attempt := 1; attempt <= startAttempts; attempt++ {
			err := s.crasher.Start(ctx)
			if err == nil {
				s.state.Notice("Matching service restarted. Kafka hands its queue back once the old session expires, usually within 30 s")
				return
			}
			log.Printf("dashboard: restart attempt %d: %v", attempt, err)
			time.Sleep(time.Second)
		}
		s.state.Notice("Couldn't restart the matching service. Run: docker compose up -d")
	}()
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("dashboard: write response: %v", err)
	}
}
