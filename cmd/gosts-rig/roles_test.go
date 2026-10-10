package main

import (
	"context"
	"path/filepath"
	"testing"
)

// Servo Ctl's one group of two of the three servos found is the elevation,
// the third the azimuth; otherwise the roles stay.
func TestRolesFromGroups(t *testing.T) {
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{cfg: cfg, out: func(any) {}}
	if err := r.connect(context.Background(), simPort, 0); err != nil { // servos 1, 2, 3
		t.Fatal(err)
	}
	defer r.disconnect()
	mirrored := func(id uint8) bool { return id == 3 }
	before := cfg.get().Roles

	r.rolesFromGroups([][]uint8{{1, 2}, {2, 3}}, mirrored) // two groups: unclear
	r.rolesFromGroups([][]uint8{{1, 2, 3}}, mirrored)      // not two servos
	r.rolesFromGroups([][]uint8{{3, 9}}, mirrored)         // 9 isn't there
	if cfg.get().Roles != before {
		t.Fatalf("roles changed: %+v", cfg.get().Roles)
	}

	r.rolesFromGroups([][]uint8{{3, 1}}, mirrored)
	ro := cfg.get().Roles
	if ro.ElevationLeader != 3 || ro.ElevationFollower != 1 || ro.Azimuth != 2 || !ro.LeaderMirrored || ro.FollowerMirrored {
		t.Fatalf("roles: %+v", ro)
	}
}

// Servos another scan found (Servo Ctl's: turned on after the rig's own scan)
// become the rig's, and its state says so.
func TestAdoptFound(t *testing.T) {
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var states int
	r := &rig{cfg: cfg, out: func(m any) {
		if _, ok := m.(stateMsg); ok {
			states++
		}
	}}
	if err := r.connect(context.Background(), simPort, 0); err != nil {
		t.Fatal(err)
	}
	defer r.disconnect()
	r.mu.Lock()
	r.found = nil // as if they'd been off at the rig's scan
	r.mu.Unlock()
	before := states
	r.adoptFound([]uint8{3, 1, 2})
	if got := r.state().Found; len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("found %v", got)
	}
	if states == before {
		t.Fatal("no state sent")
	}
	if _, err := r.ready(); err != nil {
		t.Fatalf("roles' servos: %v", err)
	}
}
