package app

import (
	"reflect"
	"runtime"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfig(t *testing.T) {
	defaults := Config{
		MetricsAddr:   ":2112",
		KafkaBrokers:  []string{"localhost:9092"},
		RedisAddr:     "localhost:6379",
		MatchWorkers:  runtime.NumCPU() * 25,
		MatchDeadline: 500 * time.Millisecond,
	}
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{"defaults", nil, defaults, false},
		{"overrides", map[string]string{
			"METRICS_ADDR": ":9", "KAFKA_BROKERS": "a:1, b:2", "REDIS_ADDR": "r:6",
			"MATCH_WORKERS": "7", "MATCH_DEADLINE": "1s",
		}, Config{
			MetricsAddr: ":9", KafkaBrokers: []string{"a:1", "b:2"}, RedisAddr: "r:6",
			MatchWorkers: 7, MatchDeadline: time.Second,
		}, false},
		{"empty broker entry", map[string]string{"KAFKA_BROKERS": "a:1,,b:2"}, Config{}, true},
		{"workers zero", map[string]string{"MATCH_WORKERS": "0"}, Config{}, true},
		{"workers negative", map[string]string{"MATCH_WORKERS": "-3"}, Config{}, true},
		{"workers not a number", map[string]string{"MATCH_WORKERS": "many"}, Config{}, true},
		{"deadline not a duration", map[string]string{"MATCH_DEADLINE": "500"}, Config{}, true},
		{"deadline equals batch deadline", map[string]string{"MATCH_DEADLINE": "200ms"}, Config{}, true},
		{"deadline below batch deadline", map[string]string{"MATCH_DEADLINE": "100ms"}, Config{}, true},
		{"deadline just above batch deadline", map[string]string{"MATCH_DEADLINE": "201ms"},
			func() Config { c := defaults; c.MatchDeadline = 201 * time.Millisecond; return c }(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadConfig(env(tt.env))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
