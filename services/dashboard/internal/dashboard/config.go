// Package dashboard is the demo's live view of RidePulse: it generates simulated
// rides, reads what the services really do, and serves one page showing it.
package dashboard

import (
	"fmt"
	"strconv"
	"strings"
)

// Config holds the dashboard's environment-derived settings.
type Config struct {
	Addr          string
	KafkaBrokers  []string
	RedisAddr     string
	GatewayURL    string
	MetricsURLs   map[string]string
	InitialDemand int
	DockerSocket  string
	CrashTarget   string
}

// LoadConfig reads the settings via getenv. The defaults match compose.yaml.
func LoadConfig(getenv func(string) string) (Config, error) {
	env := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	var brokers []string
	for _, b := range strings.Split(env("KAFKA_BROKERS", "kafka:9092"), ",") {
		b = strings.TrimSpace(b)
		if b == "" {
			return Config{}, fmt.Errorf("KAFKA_BROKERS contains an empty entry")
		}
		brokers = append(brokers, b)
	}

	demand, err := strconv.Atoi(env("DEMO_DEMAND", "30"))
	if err != nil || demand < 0 {
		return Config{}, fmt.Errorf("DEMO_DEMAND must be a non-negative integer")
	}

	return Config{
		Addr:         env("DASHBOARD_ADDR", ":3000"),
		KafkaBrokers: brokers,
		RedisAddr:    env("REDIS_ADDR", "redis:6379"),
		GatewayURL:   env("GATEWAY_URL", "http://api-gateway:8080/rides"),
		MetricsURLs: map[string]string{
			"gateway":    env("GATEWAY_METRICS_URL", "http://api-gateway:8080/metrics"),
			"pricing":    env("PRICING_METRICS_URL", "http://pricing:2115/metrics"),
			"matching":   env("MATCHING_METRICS_URL", "http://matching:2112/metrics"),
			"assignment": env("ASSIGNMENT_METRICS_URL", "http://assignment:2114/metrics"),
		},
		InitialDemand: demand,
		DockerSocket:  env("DOCKER_SOCKET", "/var/run/docker.sock"),
		CrashTarget:   env("CRASH_TARGET", "ridepulse-matching"),
	}, nil
}
