package dashboard

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeControls struct {
	badErr     error
	redelivers bool
	frames     chan []byte
}

func (f *fakeControls) BadMessage(context.Context) error { return f.badErr }

func (f *fakeControls) Redeliver(context.Context) (string, string, bool, error) {
	return "ride-12345678", "driver-7", f.redelivers, nil
}

func (f *fakeControls) Subscribe() (<-chan []byte, func()) { return f.frames, func() {} }

func (f *fakeControls) Recording() []byte { return []byte(`[{"at":1}]`) }

func newTestServer(controls Controls, crasher *Crasher) (*httptest.Server, *Load, *State) {
	state := NewState()
	load := NewLoad("", state, 10)
	srv := httptest.NewServer(NewServer(controls, load, state, crasher).Handler())
	return srv, load, state
}

func post(t *testing.T, url string, withHeader bool) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if withHeader {
		req.Header.Set(controlHeader, "1")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestControlsRejectRequestsWithoutHeader(t *testing.T) {
	srv, _, _ := newTestServer(&fakeControls{}, nil)
	defer srv.Close()
	for _, path := range []string{"/api/demand?rps=5", "/api/rush", "/api/bad-message", "/api/redeliver", "/api/crash"} {
		if code := post(t, srv.URL+path, false); code != http.StatusForbidden {
			t.Errorf("%s without header = %d, want 403", path, code)
		}
	}
}

func TestDemandControl(t *testing.T) {
	srv, load, _ := newTestServer(&fakeControls{}, nil)
	defer srv.Close()
	if code := post(t, srv.URL+"/api/demand?rps=120", true); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if load.Demand() != 120 {
		t.Fatalf("demand = %d", load.Demand())
	}
	if code := post(t, srv.URL+"/api/demand?rps=lots", true); code != http.StatusBadRequest {
		t.Fatalf("bad rps status = %d", code)
	}
}

func TestBadMessageReportsFailure(t *testing.T) {
	srv, _, _ := newTestServer(&fakeControls{badErr: errors.New("kafka down")}, nil)
	defer srv.Close()
	if code := post(t, srv.URL+"/api/bad-message", true); code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", code)
	}
}

func TestRedeliverNeedsAMatchedRide(t *testing.T) {
	srv, _, _ := newTestServer(&fakeControls{}, nil)
	defer srv.Close()
	if code := post(t, srv.URL+"/api/redeliver", true); code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", code)
	}
}

func TestCrashUnavailableWithoutDocker(t *testing.T) {
	srv, _, _ := newTestServer(&fakeControls{}, nil)
	defer srv.Close()
	if code := post(t, srv.URL+"/api/crash", true); code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
}

func TestCrashKillsThenRestarts(t *testing.T) {
	calls := make(chan string, 4)
	docker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls <- r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer docker.Close()

	srv, _, state := newTestServer(&fakeControls{}, testCrasher(docker))
	defer srv.Close()
	if code := post(t, srv.URL+"/api/crash", true); code != http.StatusNoContent {
		t.Fatalf("status = %d", code)
	}
	if code := post(t, srv.URL+"/api/crash", true); code != http.StatusConflict {
		t.Fatalf("second crash during restart = %d, want 409", code)
	}
	if got := <-calls; got != "/containers/ridepulse-matching/kill" {
		t.Fatalf("first call = %s", got)
	}
	select {
	case got := <-calls:
		if got != "/containers/ridepulse-matching/start" {
			t.Fatalf("second call = %s", got)
		}
	case <-time.After(crashDowntime + 2*time.Second):
		t.Fatal("matching was not restarted")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var f Frame
		state.FillFrame(&f, nil, time.Now())
		if len(f.Notices) == 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("restart notice never appeared")
}

func TestStreamSendsFrames(t *testing.T) {
	frames := make(chan []byte, 1)
	frames <- []byte(`{"at":42}`)
	srv, _, _ := newTestServer(&fakeControls{frames: frames}, nil)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != `data: {"at":42}` {
		t.Fatalf("line = %q, err = %v", line, err)
	}
}

func TestServesThePageAndConfig(t *testing.T) {
	srv, _, _ := newTestServer(&fakeControls{}, nil)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("page: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	resp, err = http.Get(srv.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("config status %d", resp.StatusCode)
	}
}

func TestLagOf(t *testing.T) {
	cases := []struct{ end, committed, want int64 }{
		{100, 90, 10},
		{100, 100, 0},
		{100, -1, 100}, // the group hasn't committed yet
		{90, 100, 0},
	}
	for _, c := range cases {
		if got := lagOf(c.end, c.committed); got != c.want {
			t.Errorf("lagOf(%d, %d) = %d, want %d", c.end, c.committed, got, c.want)
		}
	}
}
