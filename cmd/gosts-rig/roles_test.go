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

	r.rolesFromGroups([][]uint8{{3, 1}}, mirrored) // the simulator: its roles are its own
	if cfg.get().Roles != before || r.roles() != simRoles(before) {
		t.Fatalf("the simulator's roles changed: %+v, %+v", cfg.get().Roles, r.roles())
	}
	r.mu.Lock()
	r.port = "/dev/cu.test" // (as if a real board: its roles the config's)
	r.mu.Unlock()

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

// The simulator's servos are 1, 2 and 3 (1 and 2 the elevation, 1 mirrored),
// whatever the real rig's roles in the config are.
func TestSimRoles(t *testing.T) {
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.update(func(c *Config) { c.Roles = Roles{ElevationLeader: 10, ElevationFollower: 11, Azimuth: 12, FollowerMirrored: true} })
	r := &rig{cfg: cfg, out: func(any) {}}
	if err := r.connect(context.Background(), simPort, 0); err != nil {
		t.Fatal(err)
	}
	defer r.disconnect()
	if got := r.state().Found; len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("found %v, want [1 2 3]", got)
	}
	ro := r.state().Config.Roles
	if ro.ElevationLeader != 1 || ro.ElevationFollower != 2 || ro.Azimuth != 3 || !ro.LeaderMirrored || ro.FollowerMirrored {
		t.Fatalf("roles %+v", ro)
	}
	if _, err := r.ready(); err != nil {
		t.Fatal(err)
	}
	if cfg.get().Roles.ElevationLeader != 10 {
		t.Fatal("the real rig's roles changed")
	}
	if r.setRoles(Roles{ElevationLeader: 2, ElevationFollower: 1, Azimuth: 3}) == nil {
		t.Fatal("the simulator's roles set")
	}
}
