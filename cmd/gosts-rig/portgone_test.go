package main

import (
	"errors"
	"fmt"
	"syscall"
	"testing"

	"github.com/frifox/gosts"
)

// The port failing (the board off USB: seen as "device not configured"
// mid-capture) is told from a servo not answering: the one stops a moving
// path at once and reconnects, the other's ridden out.
func TestPortGone(t *testing.T) {
	for _, tc := range []struct {
		err  error
		gone bool
	}{
		{nil, false},
		{fmt.Errorf("gosts: servo 4: %w", gosts.ErrTimeout), false},
		{errors.Join(fmt.Errorf("gosts: servo 2: %w", gosts.ErrTimeout), fmt.Errorf("gosts: servo 4: %w", gosts.ErrTimeout)), false},
		{fmt.Errorf("gosts: write: %w", syscall.ENXIO), true},
		{fmt.Errorf("servo 3: %w", fmt.Errorf("gosts: write: %w", syscall.ENXIO)), true},
		{errors.New("gosts: read: device not configured"), true}, // (a port error not unwrapping)
		{errNoBoard, true},
	} {
		if got := portGone(tc.err); got != tc.gone {
			t.Errorf("portGone(%v) = %v, want %v", tc.err, got, tc.gone)
		}
	}
}
