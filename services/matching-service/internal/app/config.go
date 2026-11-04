package app

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// defaultMatchDeadline bounds one driver claim against Redis.
const defaultMatchDeadline = 500 * time.Millisecond

// Config holds matching-service's environment-derived settings.
type Config struct {
	MetricsAddr   string
	KafkaBrokers  []string
	RedisAddr     string
	MatchWorkers  int
	RedisPoolSize int
	MatchDeadline time.Duration
}

// LoadConfig reads the service settings via getenv and validates them.
func LoadConfig(getenv func(string) string) (Config, error) {
	brokers, err := parseBrokers(envOr(getenv, "KAFKA_BROKERS", "localhost:9092"))
	if err != nil {
		return Config{}, err
	}

	workers := runtime.NumCPU() * 25
	if raw := strings.TrimSpace(getenv("MATCH_WORKERS")); raw != "" {
		workers, err = strconv.Atoi(raw)
		if err != nil || workers <= 0 {
			return Config{}, fmt.Errorf("MATCH_WORKERS %q must be a positive integer", raw)
		}
	}

	// Each worker holds one Redis connection at a time.
	poolSize := workers
	if raw := strings.TrimSpace(getenv("REDIS_POOL_SIZE")); raw != "" {
		poolSize, err = strconv.Atoi(raw)
		if err != nil || poolSize <= 0 {
			return Config{}, fmt.Errorf("REDIS_POOL_SIZE %q must be a positive integer", raw)
		}
	}

	deadline := defaultMatchDeadline
	if raw := strings.TrimSpace(getenv("MATCH_DEADLINE")); raw != "" {
		deadline, err = time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("MATCH_DEADLINE %q is not a valid duration: %w", raw, err)
		}
		if deadline <= 0 {
			return Config{}, fmt.Errorf("MATCH_DEADLINE %q must be positive", raw)
		}
	}

	return Config{
		MetricsAddr:   envOr(getenv, "METRICS_ADDR", ":2112"),
		KafkaBrokers:  brokers,
		RedisAddr:     envOr(getenv, "REDIS_ADDR", "localhost:6379"),
		MatchWorkers:  workers,
		RedisPoolSize: poolSize,
		MatchDeadline: deadline,
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
