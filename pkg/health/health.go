// Package health provides the liveness and readiness HTTP handlers shared by
// the RidePulse services.
//
// Liveness answers "is the process serving HTTP". It never touches
// dependencies, so a Kafka outage does not make an orchestrator restart every
// service. Readiness answers "can this instance do useful work now" by running
// cheap dependency checks with a short overall timeout.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/segmentio/kafka-go"
)

// Check reports whether one dependency is usable. It should return promptly
// once ctx is done.
type Check func(ctx context.Context) error

// Liveness returns 200 while the process is running.
func Liveness() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}
}

// Readiness returns 200 only when every check passes within timeout. The
// checks run concurrently. A failing or timed-out check gives 503 and its name
// appears under "failing" in the body.
func Readiness(timeout time.Duration, checks map[string]Check) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		failing := runChecks(ctx, checks)
		if len(failing) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":  "unavailable",
			"failing": failing,
		})
	}
}

// runChecks runs every check concurrently and returns the failure message for
// each one that failed. A check still running when ctx ends is reported as
// timed out, so a check that ignores its context cannot hang the handler.
func runChecks(ctx context.Context, checks map[string]Check) map[string]string {
	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(checks))
	for name, check := range checks {
		go func() { results <- result{name, check(ctx)} }()
	}

	failing := make(map[string]string)
	answered := make(map[string]bool, len(checks))
	for len(answered) < len(checks) {
		select {
		case res := <-results:
			answered[res.name] = true
			if res.err != nil {
				failing[res.name] = res.err.Error()
			}
		case <-ctx.Done():
			for name := range checks {
				if !answered[name] {
					failing[name] = "timed out"
				}
			}
			return failing
		}
	}
	return failing
}

// Kafka returns a Check that succeeds when at least one broker accepts a TCP
// connection. It does not create or inspect topics.
func Kafka(brokers []string) Check {
	return func(ctx context.Context) error {
		if len(brokers) == 0 {
			return errors.New("no brokers configured")
		}
		var errs []error
		for _, broker := range brokers {
			conn, err := kafka.DialContext(ctx, "tcp", broker)
			if err == nil {
				_ = conn.Close()
				return nil
			}
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
