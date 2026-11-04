package app

import (
	"fmt"
	"strings"
)

// Config holds pricing-service's environment-derived settings.
type Config struct {
	MetricsAddr  string
	KafkaBrokers []string
}

// LoadConfig reads METRICS_ADDR and KAFKA_BROKERS via getenv.
func LoadConfig(getenv func(string) string) (Config, error) {
	brokers, err := parseBrokers(envOr(getenv, "KAFKA_BROKERS", "localhost:9092"))
	if err != nil {
		return Config{}, err
	}
	return Config{
		MetricsAddr:  envOr(getenv, "METRICS_ADDR", ":2115"),
		KafkaBrokers: brokers,
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
