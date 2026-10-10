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
