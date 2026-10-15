package app

import (
	"reflect"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{"defaults", nil, Config{MetricsAddr: ":2115", KafkaBrokers: []string{"localhost:9092"}}, false},
		{"overrides", map[string]string{"METRICS_ADDR": ":9000", "KAFKA_BROKERS": "a:1, b:2"},
			Config{MetricsAddr: ":9000", KafkaBrokers: []string{"a:1", "b:2"}}, false},
		{"empty broker entry", map[string]string{"KAFKA_BROKERS": "a:1,,b:2"}, Config{}, true},
		{"trailing comma", map[string]string{"KAFKA_BROKERS": "a:1,"}, Config{}, true},
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
