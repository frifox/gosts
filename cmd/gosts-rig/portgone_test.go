package main

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

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

// The servos' supply as recorded on the rig at Go 0°: 12.2 V, 5.0 V as the
// elevation started, silent 2.5 s (restarting), 12.2 V again: said once,
// as a sag to 5 V (a USB-C PD charger's), with the silence.
func TestPowerSag(t *testing.T) {
	var p powerSag
	at := time.Date(2026, 10, 30, 17, 40, 0, 0, time.UTC)
	tick := func(ms int, v ...float64) string {
		for i, x := range v {
			p.seen(uint8(2+i), x)
		}
		return p.check(at.Add(time.Duration(ms)*time.Millisecond), 3)
	}
	for ms := 0; ms < 300; ms += 100 {
		if s := tick(ms, 12.2, 12.2, 12.2); s != "" {
			t.Fatalf("steady: %q", s)
		}
	}
	if s := tick(290, 11.3, 5.0, 5.4); s != "" {
		t.Fatalf("in the sag: %q (said once it's over)", s)
	}
	for ms := 905; ms < 2800; ms += 550 {
		if s := tick(ms); s != "" { // (none answering)
			t.Fatalf("silent: %q", s)
		}
	}
	s := tick(2805, 12.2, 12.2, 12.2)
	for _, want := range []string{"5.0 V", "12.2 V", "silent 1.9 s", "USB-C PD"} {
		if !strings.Contains(s, want) {
			t.Errorf("%q: no %q", s, want)
		}
	}
	if s := tick(2905, 12.2, 12.2, 12.2); s != "" {
		t.Errorf("said again: %q", s)
	}
	if s := tick(3005, 11.8, 12.0, 12.1); s != "" {
		t.Errorf("a small dip: %q", s)
	}
}
