package agent

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsClosedProbeError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "sdk closed", err: errors.New("E_CLOSED: EOF"), want: true},
		{name: "runtime session closed", err: errors.New("pi sdk runtime session closed"), want: true},
		{name: "wrapped eof", err: fmt.Errorf("list models: %w", errors.New("EOF")), want: true},
		{name: "broken pipe", err: errors.New("write |1: broken pipe"), want: true},
		{name: "auth error", err: errors.New("unauthorized: missing api key"), want: false},
		{name: "model error", err: errors.New("model must be provider/modelId"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isClosedProbeError(tt.err); got != tt.want {
				t.Fatalf("isClosedProbeError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
