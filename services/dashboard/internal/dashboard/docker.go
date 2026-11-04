package dashboard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Crasher force-kills one fixed container and starts it again through the
// Docker API. It can't act on any other container.
type Crasher struct {
	container string
	client    *http.Client
	base      string
}

// NewCrasher talks to the Docker socket at path.
func NewCrasher(socket, container string) *Crasher {
	return &Crasher{
		container: container,
		base:      "http://docker",
		client: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			}},
		},
	}
}

// Available reports whether Docker answers. The live demo has it; anywhere else
// the crash button is hidden.
func (c *Crasher) Available(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/_ping", nil)
	if err != nil {
		return false
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Kill force-kills the container, with no chance to shut down cleanly.
func (c *Crasher) Kill(ctx context.Context) error {
	return c.post(ctx, "/containers/"+url.PathEscape(c.container)+"/kill?signal=SIGKILL")
}

// Start starts the container again.
func (c *Crasher) Start(ctx context.Context) error {
	return c.post(ctx, "/containers/"+url.PathEscape(c.container)+"/start")
}

func (c *Crasher) post(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 304 from start means it's already running, for example via a restart policy.
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		return fmt.Errorf("docker %s: %s", path, resp.Status)
	}
	return nil
}
