// benchmark walks an RPS ladder against a running RidePulse cluster,
// driving the rider-simulator per step and querying Prometheus at the end
// of each measurement window. Emits one CSV row per step.
//
// Usage (from tools/benchmark):
//
//	go run cmd/benchmark/main.go
//	go run cmd/benchmark/main.go --ladder=100:40,300:120,600:250 --window=45s
//
// Assumes docker-compose (Kafka/Redis/Prometheus) and the four Go services
// (api-gateway, pricing-service, matching-service, assignment-service) are
// already running. Only the rider-simulator is driven by this tool.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type ladderStep struct {
	rps         int
	concurrency int
}

func parseLadder(s string) ([]ladderStep, error) {
	var out []ladderStep
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.Split(part, ":")
		if len(kv) != 2 {
			return nil, fmt.Errorf("bad ladder step %q (want RPS:CONCURRENCY)", part)
		}
		r, err := strconv.Atoi(kv[0])
		if err != nil {
			return nil, fmt.Errorf("bad RPS in %q: %w", part, err)
		}
		c, err := strconv.Atoi(kv[1])
		if err != nil {
			return nil, fmt.Errorf("bad concurrency in %q: %w", part, err)
		}
		out = append(out, ladderStep{rps: r, concurrency: c})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty ladder")
	}
	return out, nil
}

// riderMu guards currentRider so the signal handler can kill it cleanly.
var (
	riderMu       sync.Mutex
	currentRider  *exec.Cmd
	shutdownOnce  sync.Once
	shutdownFlag  = make(chan struct{})
)

func main() {
	var (
		promURL   = flag.String("prom-url", "http://localhost:9090", "Prometheus base URL")
		riderCwd  = flag.String("rider-cwd", "../../services/rider-simulator", "Working directory of rider-simulator module")
		outPath   = flag.String("output", "benchmark-results.csv", "CSV output path")
		ladderStr = flag.String("ladder", "50:20,100:40,200:80,300:120,500:200,800:300", "RPS:concurrency steps, comma-separated")
		warmup    = flag.Duration("warmup", 15*time.Second, "Wait after starting rider-sim before measurement begins")
		window    = flag.Duration("window", 30*time.Second, "Measurement window duration")
		cooldown  = flag.Duration("cooldown", 10*time.Second, "Wait between steps (drain in-flight)")
	)
	flag.Parse()

	steps, err := parseLadder(*ladderStr)
	if err != nil {
		log.Fatalf("bad --ladder: %v", err)
	}

	prom := &promClient{baseURL: *promURL, http: &http.Client{Timeout: 5 * time.Second}}
	if err := prom.ping(); err != nil {
		log.Fatalf("prometheus not reachable at %s: %v", *promURL, err)
	}

	// Build rider-simulator once so per-step startup is fast.
	riderBin, err := buildRider(*riderCwd)
	if err != nil {
		log.Fatalf("failed to build rider-simulator: %v", err)
	}
	defer os.Remove(riderBin)

	// Ctrl+C handler: kill the running rider-sim and exit non-zero.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("interrupt received; killing rider-sim and aborting")
		killRider()
		close(shutdownFlag)
	}()

	out, err := os.Create(*outPath)
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()

	w := csv.NewWriter(out)
	defer w.Flush()

	header := []string{
		"step", "target_rps", "target_concurrency",
		"actual_rps_in", "assignments_rps",
		"match_success_rps", "match_failures_rps",
		"publish_errors_rps",
		"lock_conflicts_rps", "phantom_releases_rps",
		"e2e_p50_ms", "e2e_p95_ms", "e2e_p99_ms",
		"gateway_p95_ms", "match_p95_ms",
		"success_rate_pct",
	}
	if err := w.Write(header); err != nil {
		log.Fatal(err)
	}
	w.Flush()

	totalStart := time.Now()

	for i, step := range steps {
		select {
		case <-shutdownFlag:
			log.Println("aborted before completing ladder")
			return
		default:
		}

		log.Printf("=== step %d/%d: target %d RPS, concurrency %d ===",
			i+1, len(steps), step.rps, step.concurrency)

		if err := startRider(riderBin, step); err != nil {
			log.Fatalf("failed to start rider-sim: %v", err)
		}

		if !sleepInterruptible(*warmup) {
			return
		}
		log.Printf("... measurement window %s ...", *window)
		if !sleepInterruptible(*window) {
			return
		}

		row := prom.collect(step, *window)

		killRider()

		row = append([]string{strconv.Itoa(i + 1)}, row...)
		if err := w.Write(row); err != nil {
			log.Fatal(err)
		}
		w.Flush()
		printStepSummary(i+1, step, row)

		if i < len(steps)-1 {
			log.Printf("... cooldown %s ...", *cooldown)
			if !sleepInterruptible(*cooldown) {
				return
			}
		}
	}

	log.Printf("ladder complete in %s; results in %s", time.Since(totalStart).Round(time.Second), *outPath)
}

// ---- subprocess management ----

func buildRider(cwd string) (string, error) {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	bin := filepath.Join(os.TempDir(), "ridepulse-rider-sim")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	log.Printf("building rider-simulator → %s", bin)
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/rider-simulator")
	cmd.Dir = absCwd
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return bin, nil
}

func startRider(bin string, step ladderStep) error {
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"RIDER_RPS="+strconv.Itoa(step.rps),
		"RIDER_CONCURRENCY="+strconv.Itoa(step.concurrency),
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	riderMu.Lock()
	currentRider = cmd
	riderMu.Unlock()
	return nil
}

func killRider() {
	riderMu.Lock()
	cmd := currentRider
	currentRider = nil
	riderMu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

func sleepInterruptible(d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-shutdownFlag:
		return false
	}
}

// ---- prometheus ----

type promClient struct {
	baseURL string
	http    *http.Client
}

type promResp struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string       `json:"resultType"`
		Result     []promResult `json:"result"`
	} `json:"data"`
}

type promResult struct {
	Metric map[string]string `json:"metric"`
	Value  [2]interface{}    `json:"value"`
}

func (p *promClient) ping() error {
	_, err := p.query("up")
	return err
}

func (p *promClient) query(q string) (float64, error) {
	u := fmt.Sprintf("%s/api/v1/query?query=%s", p.baseURL, url.QueryEscape(q))
	resp, err := p.http.Get(u)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var pr promResp
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return 0, err
	}
	if pr.Status != "success" {
		return 0, fmt.Errorf("prom status: %s", pr.Status)
	}
	if len(pr.Data.Result) == 0 {
		// Metric not present or no samples in window yet. Treat as 0.
		return 0, nil
	}
	s, ok := pr.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		// "NaN" happens for histogram_quantile with no samples. Treat as 0.
		return 0, nil
	}
	return f, nil
}

func (p *promClient) queryOrLog(expr string) float64 {
	v, err := p.query(expr)
	if err != nil {
		log.Printf("prom query failed: %s: %v", expr, err)
		return 0
	}
	return v
}

func (p *promClient) collect(step ladderStep, window time.Duration) []string {
	w := formatDuration(window)

	actualRPS := p.queryOrLog(fmt.Sprintf("rate(gateway_requests_total[%s])", w))
	assignments := p.queryOrLog(fmt.Sprintf("rate(assignments_total[%s])", w))
	matchSuccess := p.queryOrLog(fmt.Sprintf("rate(matching_success_total[%s])", w))
	matchFailures := p.queryOrLog(fmt.Sprintf("rate(matching_failures_total[%s])", w))
	publishErrors := p.queryOrLog(fmt.Sprintf("rate(gateway_publish_errors_total[%s])", w))
	lockConflicts := p.queryOrLog(fmt.Sprintf("rate(driver_lock_conflict_total[%s])", w))
	phantomReleases := p.queryOrLog(fmt.Sprintf("rate(driver_lock_released_phantom_total[%s])", w))

	e2eP50 := p.queryOrLog(fmt.Sprintf("histogram_quantile(0.50, sum by (le) (rate(end_to_end_latency_seconds_bucket[%s])))", w))
	e2eP95 := p.queryOrLog(fmt.Sprintf("histogram_quantile(0.95, sum by (le) (rate(end_to_end_latency_seconds_bucket[%s])))", w))
	e2eP99 := p.queryOrLog(fmt.Sprintf("histogram_quantile(0.99, sum by (le) (rate(end_to_end_latency_seconds_bucket[%s])))", w))

	gwP95 := p.queryOrLog(fmt.Sprintf("histogram_quantile(0.95, sum by (le) (rate(gateway_request_duration_seconds_bucket[%s])))", w))
	matchP95 := p.queryOrLog(fmt.Sprintf("histogram_quantile(0.95, sum by (le) (rate(matching_latency_seconds_bucket[%s])))", w))

	var successRate float64
	if actualRPS > 0 {
		successRate = 100 * assignments / actualRPS
		if successRate > 100 {
			// Assignments observed can briefly exceed requests due to
			// the metric being an average over the window with in-flight
			// rides carrying over. Clamp for readability.
			successRate = 100
		}
	}

	return []string{
		strconv.Itoa(step.rps),
		strconv.Itoa(step.concurrency),
		fmt.Sprintf("%.1f", actualRPS),
		fmt.Sprintf("%.1f", assignments),
		fmt.Sprintf("%.1f", matchSuccess),
		fmt.Sprintf("%.1f", matchFailures),
		fmt.Sprintf("%.2f", publishErrors),
		fmt.Sprintf("%.1f", lockConflicts),
		fmt.Sprintf("%.1f", phantomReleases),
		fmt.Sprintf("%.0f", e2eP50*1000),
		fmt.Sprintf("%.0f", e2eP95*1000),
		fmt.Sprintf("%.0f", e2eP99*1000),
		fmt.Sprintf("%.0f", gwP95*1000),
		fmt.Sprintf("%.0f", matchP95*1000),
		fmt.Sprintf("%.1f", successRate),
	}
}

func formatDuration(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return d.String()
}

// printStepSummary writes a compact human-readable summary of the row that
// was just recorded.
func printStepSummary(step int, ls ladderStep, row []string) {
	// row layout matches the CSV columns MINUS the leading "step" column.
	log.Printf(
		"step %d done: target=%d rps → in=%s rps, assign=%s rps (success=%s%%), e2e p95=%s ms, match p95=%s ms, lock_conflicts=%s/s, phantom_rel=%s/s",
		step, ls.rps,
		row[2], row[3], row[14], row[10], row[13], row[7], row[8],
	)
}
