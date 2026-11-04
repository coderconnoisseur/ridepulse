package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testCrasher(srv *httptest.Server) *Crasher {
	return &Crasher{container: "ridepulse-matching", base: srv.URL, client: srv.Client()}
}

func TestCrasherTargetsOnlyItsContainer(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := testCrasher(srv)
	if err := c.Kill(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /containers/ridepulse-matching/kill?signal=SIGKILL",
		"POST /containers/ridepulse-matching/start",
	}
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

func TestCrasherErrorsAndAlreadyRunning(t *testing.T) {
	status := http.StatusInternalServerError
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()

	c := testCrasher(srv)
	if err := c.Kill(context.Background()); err == nil {
		t.Fatal("want an error from a failed kill")
	}
	status = http.StatusNotModified
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("already running should be fine: %v", err)
	}
}

func TestCrasherAvailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	c := testCrasher(srv)
	if !c.Available(context.Background()) {
		t.Fatal("should be available")
	}
	srv.Close()
	if c.Available(context.Background()) {
		t.Fatal("should be unavailable once Docker is gone")
	}
}
