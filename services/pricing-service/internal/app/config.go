package app

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
)

// Config holds pricing-service's environment-derived settings.
type Config struct {
	MetricsAddr  string
	KafkaBrokers []string
	RedisAddr    string
	Workers      int
}

// LoadConfig reads METRICS_ADDR, KAFKA_BROKERS, REDIS_ADDR and PRICING_WORKERS via getenv.
func LoadConfig(getenv func(string) string) (Config, error) {
	brokers, err := parseBrokers(envOr(getenv, "KAFKA_BROKERS", "localhost:9092"))
	if err != nil {
		return Config{}, err
	}

	// Each worker blocks on a Kafka publish round trip, so the pool size caps
	// throughput; pricing itself uses almost no CPU.
	workers := runtime.NumCPU() * 25
	if raw := strings.TrimSpace(getenv("PRICING_WORKERS")); raw != "" {
		workers, err = strconv.Atoi(raw)
		if err != nil || workers <= 0 {
			return Config{}, fmt.Errorf("PRICING_WORKERS %q must be a positive integer", raw)
		}
	}

	return Config{
		MetricsAddr:  envOr(getenv, "METRICS_ADDR", ":2115"),
		KafkaBrokers: brokers,
		RedisAddr:    envOr(getenv, "REDIS_ADDR", "localhost:6379"),
		Workers:      workers,
	}, nil
}

func envOr(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

// parseBrokers splits a comma-separated broker list, trimming spaces and
// rejecting empty entries.
func parseBrokers(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	brokers := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("KAFKA_BROKERS %q contains an empty entry", raw)
		}
		brokers = append(brokers, p)
	}
	return brokers, nil
}
