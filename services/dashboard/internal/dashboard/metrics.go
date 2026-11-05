package dashboard

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// parseMetrics reads Prometheus text format and keeps the metrics in want.
// Label variants of one metric are summed.
func parseMetrics(r io.Reader, want map[string]bool) (map[string]float64, error) {
	out := make(map[string]float64)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name, _, _ := strings.Cut(fields[0], "{")
		if !want[name] {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		out[name] += v
	}
	return out, sc.Err()
}

// rateWindow is how many one-second samples a rate is averaged over.
const rateWindow = 5

// counter turns a service's counter into a total since the dashboard started
// plus a recent rate. A service restart resets its counter to zero, so a drop
// counts as new growth instead of a negative rate.
type counter struct {
	total   float64
	prev    float64
	seen    bool
	history [rateWindow + 1]float64
	next    int
	samples int
}

// observe records the counter's current value, if the service answered.
func (c *counter) observe(v float64) {
	switch {
	case !c.seen:
		c.seen = true
	case v >= c.prev:
		c.total += v - c.prev
	default:
		c.total += v
	}
	c.prev = v
}

// tick closes a one-second sample. It runs whether or not the service
// answered, so the rate falls to zero while a service is down.
func (c *counter) tick() {
	c.history[c.next] = c.total
	c.next = (c.next + 1) % len(c.history)
	if c.samples < len(c.history) {
		c.samples++
	}
}

// rate returns the average per-second growth over the recent samples.
func (c *counter) rate() float64 {
	if c.samples < 2 {
		return 0
	}
	newest := c.history[(c.next-1+len(c.history))%len(c.history)]
	oldest := c.history[(c.next-c.samples+len(c.history))%len(c.history)]
	return (newest - oldest) / float64(c.samples-1)
}
