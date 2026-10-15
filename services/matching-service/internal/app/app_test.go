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
		ctx  error
		want bool
	}{
		{"no drivers", matching.ErrNoDriversAvailable, nil, true},
		{"wrapped no drivers", fmt.Errorf("match: %w", matching.ErrNoDriversAvailable), nil, true},
		{"outer match deadline", context.DeadlineExceeded, context.DeadlineExceeded, true},
		{"inner deadline, match ctx alive", context.DeadlineExceeded, nil, false},
		{"redis error", errors.New("dial tcp: connection refused"), nil, false},
		{"canceled", context.Canceled, nil, false},
	}
	for _, tc := range cases {
		if got := isBusinessFailure(tc.err, tc.ctx); got != tc.want {
			t.Errorf("%s: isBusinessFailure(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}
