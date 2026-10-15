package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"ridepulse/services/matching-service/internal/matching"
)

func TestIsBusinessFailure(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"no drivers", matching.ErrNoDriversAvailable, true},
		{"wrapped no drivers", fmt.Errorf("match: %w", matching.ErrNoDriversAvailable), true},
		{"match deadline", context.DeadlineExceeded, true},
		{"redis error", errors.New("dial tcp: connection refused"), false},
		{"canceled", context.Canceled, false},
	}
	for _, tc := range cases {
		if got := isBusinessFailure(tc.err); got != tc.want {
			t.Errorf("%s: isBusinessFailure(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}
