package dashboard

import (
	"strings"
	"testing"
)

func TestParseMetricsKeepsWantedAndSumsLabels(t *testing.T) {
	text := `# HELP unmatched_total x
# TYPE unmatched_total counter
unmatched_total{reason="no_drivers"} 4
unmatched_total{reason="other"} 1.5
assignments_total 7
ignored_total 99
not_a_number NaNx
`
	got, err := parseMetrics(strings.NewReader(text), map[string]bool{"unmatched_total": true, "assignments_total": true, "not_a_number": true})
	if err != nil {
		t.Fatal(err)
	}
	if got["unmatched_total"] != 5.5 || got["assignments_total"] != 7 {
		t.Fatalf("got %v", got)
	}
	if _, ok := got["ignored_total"]; ok {
		t.Fatal("kept an unwanted metric")
	}
	if _, ok := got["not_a_number"]; ok {
		t.Fatal("kept an unparsable value")
	}
}

func TestCounterStartsFromFirstValue(t *testing.T) {
	var c counter
	c.observe(1000) // the service was already running
	c.observe(1010)
	if c.total != 10 {
		t.Fatalf("total = %v, want 10", c.total)
	}
}

func TestCounterSurvivesServiceRestart(t *testing.T) {
	var c counter
	c.observe(100)
	c.observe(150)
	c.observe(20) // restarted: counts from zero again
	if c.total != 70 {
		t.Fatalf("total = %v, want 50 + 20", c.total)
	}
}

func TestCounterRateAveragesAndFallsWhenSilent(t *testing.T) {
	var c counter
	if c.rate() != 0 {
		t.Fatal("rate with no samples should be 0")
	}
	v := 0.0
	for range 10 {
		v += 20
		c.observe(v)
		c.tick()
	}
	if got := c.rate(); got != 20 {
		t.Fatalf("rate = %v, want 20", got)
	}
	for range rateWindow + 1 { // service down: ticks without observations
		c.tick()
	}
	if got := c.rate(); got != 0 {
		t.Fatalf("rate after silence = %v, want 0", got)
	}
}
