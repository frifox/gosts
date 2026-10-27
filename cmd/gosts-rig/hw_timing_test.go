package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/frifox/gosts"
)

// GOSTS_HW=/dev/cu.… : times moves on the real rig.
func TestHWMoveDelay(t *testing.T) {
	dev := os.Getenv("GOSTS_HW")
	if dev == "" {
		t.Skip("GOSTS_HW not set")
	}
	src, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), "Library/Application Support/gosts-rig/config.toml"))
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, src, 0o644)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{cfg: cfg, out: func(any) {}}
	if err := r.connect(context.Background(), dev, 0); err != nil {
		t.Fatal(err)
	}
	defer r.disconnect()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.pollLoop(ctx)
	time.Sleep(time.Second)
	ro := r.roles()
	pos := func(id uint8) int {
		var p int
		r.withBus(func(bus *gosts.Bus) error { p, _ = bus.Servo(id).Position(); return nil })
		return p
	}
	r.mu.Lock()
	e0, a0 := r.elevation, r.azimuth
	r.mu.Unlock()
	t.Logf("at elevation %.2f, azimuth %.2f", e0, a0)
	try := func(name string, id uint8, e, a *float64) {
		before := pos(id)
		t0 := time.Now()
		if err := r.moveTo(e, a); err != nil {
			t.Errorf("%s: %v", name, err)
			return
		}
		ret := time.Since(t0)
		for time.Since(t0) < 5*time.Second {
			if absInt(gosts.CircularDiff(pos(id), before)) >= 4 {
				t.Logf("%s: moveTo returned in %v, moving after %v", name, ret, time.Since(t0))
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		time.Sleep(1500 * time.Millisecond)
	}
	f := func(v float64) *float64 { return &v }
	for i := 0; i < 2; i++ {
		if os.Getenv("GOSTS_HW_OFF") != "" {
			r.torque(false)
			time.Sleep(time.Second)
		}
		try("elevation +3", ro.ElevationLeader, f(e0+3), nil)
		if os.Getenv("GOSTS_HW_OFF") != "" {
			r.torque(false)
			time.Sleep(time.Second)
		}
		try("elevation back", ro.ElevationLeader, f(e0), nil)
		try("azimuth +10", ro.Azimuth, nil, f(a0+10))
		try("azimuth back", ro.Azimuth, nil, f(a0))
	}
}
