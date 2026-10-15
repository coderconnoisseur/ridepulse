package app

import (
	"fmt"
	"strings"
)

// Config holds the gateway's environment-derived settings.
type Config struct {
	HTTPAddr     string
	KafkaBrokers []string
}

// LoadConfig reads HTTP_ADDR and KAFKA_BROKERS via getenv.
func LoadConfig(getenv func(string) string) (Config, error) {
	brokers, err := parseBrokers(envOr(getenv, "KAFKA_BROKERS", "localhost:9092"))
	if err != nil {
		return Config{}, err
	}
	return Config{
		HTTPAddr:     envOr(getenv, "HTTP_ADDR", ":8080"),
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
