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

// GOSTS_HW=/dev/cu.… GOSTS_HW_SWAP=1: the elevation pair's mirroring
// swapped (as changed in Servo Settings), 0° set, Torque Lock, a move: the
// two must turn opposite ways physically (they face each other), not fight.
// Each one's zero is put back after.
func TestHWMirrorSwap(t *testing.T) {
	dev := os.Getenv("GOSTS_HW")
	if dev == "" || os.Getenv("GOSTS_HW_SWAP") == "" {
		t.Skip("GOSTS_HW, GOSTS_HW_SWAP not set")
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
	ro := r.roles()
	lead, fol := ro.ElevationLeader, ro.ElevationFollower
	zeros := map[uint8]int{}
	r.withBus(func(bus *gosts.Bus) error {
		for _, id := range []uint8{lead, fol} {
			zeros[id], _ = bus.Servo(id).Zero()
		}
		return nil
	})
	defer func() {
		r.torque(false, "elevation")
		r.withBus(func(bus *gosts.Bus) error {
			for id, z := range zeros {
				if err := bus.Servo(id).SetZero(z); err != nil {
					t.Errorf("zero back on %d: %v", id, err)
				}
			}
			return nil
		})
	}()
	raw := func() map[uint8]int {
		m := map[uint8]int{}
		r.withBus(func(bus *gosts.Bus) error {
			for _, id := range []uint8{lead, fol} {
				m[id], _ = bus.Servo(id).Read(gosts.RegPresentPosition)
			}
			return nil
		})
		return m
	}
	dump := func(when string) {
		r.withBus(func(bus *gosts.Bus) error {
			for _, id := range []uint8{lead, fol} {
				sv := bus.Servo(id)
				raw, _ := sv.Read(gosts.RegPresentPosition)
				off, _ := sv.Read(gosts.RegPositionOffset)
				g, _ := sv.Read(gosts.RegGoalPosition)
				on, _ := sv.TorqueEnabled()
				rp, _ := sv.RestartedPosition()
				ab, err := sv.AbsolutePosition()
				t.Logf("  %s #%d: torque %v raw %d off %d goal %d restarted %d abs %d %v", when, id, on, raw, off, g, rp, ab, err)
			}
			return nil
		})
	}
	for round, swap := range []bool{true, true, false} {
		ro := r.roles()
		if swap {
			ro.LeaderMirrored, ro.FollowerMirrored = ro.FollowerMirrored, ro.LeaderMirrored
		}
		r.cfg.update(func(c *Config) { c.Roles = ro })
		r.withBus(func(bus *gosts.Bus) error { r.applyRoles(bus, ro); return nil })
		if swap {
			r.torque(false, "elevation")
		} else {
			r.torque(true, "elevation") // (zeroed while holding)
		}
		time.Sleep(300 * time.Millisecond)
		if err := r.withBus(func(bus *gosts.Bus) error { return bus.Group(lead, fol).SetPositionAs(0) }); err != nil {
			t.Fatal(err)
		}
		dump("zeroed")
		if err := r.torque(true, "elevation"); err != nil {
			t.Fatalf("round %d: Torque Lock: %v", round, err)
		}
		dump("locked")
		before := raw()
		if err := r.moveTo(func() *float64 { v := 3.0; return &v }(), nil); err != nil {
			t.Fatalf("round %d: move: %v", round, err)
		}
		for i := 0; i < 15; i++ {
			time.Sleep(100 * time.Millisecond)
			r.withBus(func(bus *gosts.Bus) error {
				res, _ := bus.SyncFeedback(lead, fol)
				t.Logf("    %4dms: #%d %d load %.0f moving %v | #%d %d load %.0f moving %v", (i+1)*100, lead, res[lead].Position, res[lead].Load, res[lead].Moving, fol, res[fol].Position, res[fol].Load, res[fol].Moving)
				return nil
			})
		}
		dump("moved")
		after := raw()
		var loads [2]float64
		r.withBus(func(bus *gosts.Bus) error {
			res, _ := bus.SyncFeedback(lead, fol)
			loads = [2]float64{res[lead].Load, res[fol].Load}
			return nil
		})
		dl, df := gosts.CircularDiff(after[lead], before[lead]), gosts.CircularDiff(after[fol], before[fol])
		t.Logf("round %d (leader #%d mirrored %v, #%d %v): physical %+d, %+d; loads %.0f%%, %.0f%%", round, lead, ro.LeaderMirrored, fol, ro.FollowerMirrored, dl, df, loads[0], loads[1])
		if err := r.moveTo(func() *float64 { v := 0.0; return &v }(), nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1500 * time.Millisecond)
	}
}
