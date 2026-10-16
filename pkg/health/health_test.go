package health

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func serve(h http.HandlerFunc) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec
}

func status(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	s, _ := body["status"].(string)
	return s
}

func TestLivenessAlwaysOK(t *testing.T) {
	rec := serve(Liveness())
	if rec.Code != http.StatusOK || status(t, rec) != "ok" {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestReadinessAllPass(t *testing.T) {
	rec := serve(Readiness(time.Second, map[string]Check{
		"a": func(context.Context) error { return nil },
		"b": func(context.Context) error { return nil },
	}))
	if rec.Code != http.StatusOK || status(t, rec) != "ready" {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestReadinessReportsFailingCheck(t *testing.T) {
	rec := serve(Readiness(time.Second, map[string]Check{
		"kafka": func(context.Context) error { return errors.New("connection refused") },
		"redis": func(context.Context) error { return nil },
	}))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d, want 503", rec.Code)
	}
	var body struct {
		Failing map[string]string `json:"failing"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Failing) != 1 || body.Failing["kafka"] != "connection refused" {
		t.Fatalf("failing=%v", body.Failing)
	}
}

func TestReadinessTimesOutHungCheck(t *testing.T) {
	hung := func(ctx context.Context) error {
		<-ctx.Done() // ignores everything but the deadline
		return ctx.Err()
	}
	start := time.Now()
	rec := serve(Readiness(50*time.Millisecond, map[string]Check{"hung": hung}))
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("readiness took %s, want ~50ms", elapsed)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d, want 503", rec.Code)
	}
}

func TestKafkaCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	closed := ln.Addr().String() // reuse the address after closing to get a refused port
	ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := Kafka([]string{closed})(ctx); err == nil {
		t.Fatal("closed port: want error")
	}
	if err := Kafka(nil)(ctx); err == nil {
		t.Fatal("no brokers: want error")
	}

	live, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	// First broker is dead, second is up: one reachable broker is enough.
	if err := Kafka([]string{closed, live.Addr().String()})(ctx); err != nil {
		t.Fatalf("reachable broker: %v", err)
	}
}
