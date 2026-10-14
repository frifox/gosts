package main

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frifox/gosts"
	"github.com/frifox/gosts/cmd/gosts-ctl/console"
	servosim "github.com/frifox/gosts/cmd/gosts-ctl/servo-sim"
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

// Connecting the simulator gives Servo Settings the simulator's own settings
// (#1 and #2 a group, #1 mirrored), the real rig's left as they are.
func TestSimServoSettings(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "config.toml")
	os.WriteFile(real, []byte("[10]\nMirrored = true\n\n[group.pitch]\nName = \"Pitch\"\nMembers = [10, 11]\n"), 0o644)
	ctlCfg, _, err := console.LoadConfig(real)
	if err != nil {
		t.Fatal(err)
	}
	ctl := console.New(ctlCfg, nil, 50*time.Millisecond, false)
	cfg, _ := loadConfig(filepath.Join(dir, "rig.toml"))
	r := &rig{cfg: cfg, out: func(any) {}}
	r.ctlFor = func(sim bool) {
		if sim {
			ctl.UseConfig(filepath.Join(dir, "gosts-rig-sim.toml"))
			ctl.SeedGroup("Elevation", []uint8{1, 2}, 1)
		} else {
			ctl.UseConfig(real)
		}
	}
	r.attach = ctl.Attach
	if err := r.connect(context.Background(), simPort, 0); err != nil {
		t.Fatal(err)
	}
	defer r.disconnect()
	if g := ctl.Groups(); len(g) != 1 || len(g[0]) != 2 || g[0][0] != 1 || g[0][1] != 2 {
		t.Fatalf("groups %v, want [[1 2]]", g)
	}
	if !ctl.Mirrored(1) || ctl.Mirrored(2) {
		t.Fatal("#1 should be mirrored, #2 not")
	}
	r.ctlFor(false) // (a real board's: the real settings back)
	if g := ctl.Groups(); len(g) != 1 || g[0][0] != 10 || !ctl.Mirrored(10) {
		t.Fatalf("real settings: %v", g)
	}
	if b, _ := os.ReadFile(real); !strings.Contains(string(b), "Members = [10, 11]") {
		t.Fatal("the real settings changed")
	}
}

// A servo turned by hand past the 0/4095 seam from its goal, torque off, has
// no known turn count (gosts.ErrTurnUnknown): a move gets Torque Lock on it
// first (holding where it is: the count from there), then goes.
func TestTurnCountLearned(t *testing.T) {
	var port *servosim.Port
	old := newSimPort
	newSimPort = func(ro Roles) *servosim.Port { port = newSim(ro); return port }
	defer func() { newSimPort = old }()
	cfg, _ := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	r := &rig{cfg: cfg, out: func(any) {}}
	if err := r.connect(context.Background(), simPort, 0); err != nil {
		t.Fatal(err)
	}
	defer r.disconnect()
	ro := r.roles()
	if err := r.torque(false, "azimuth"); err != nil {
		t.Fatal(err)
	}
	port.Turn(ro.Azimuth, -15) // (its goal 0: a step the other side of the seam)
	r.withBus(func(bus *gosts.Bus) error {
		if _, err := bus.Servo(ro.Azimuth).AbsolutePosition(); !errors.Is(err, gosts.ErrTurnUnknown) {
			t.Fatalf("set up: %v, want the turn count unknown", err)
		}
		return nil
	})
	if _, err := r.platformTurns(); err != nil { // (a reading: learned, torque back off)
		t.Fatal(err)
	}
	r.withBus(func(bus *gosts.Bus) error {
		if on, _ := bus.Servo(ro.Azimuth).TorqueEnabled(); on {
			t.Error("torque left on after a reading")
		}
		return nil
	})
	port.Turn(ro.Azimuth, -4096-30) // (by hand again, a turn on)
	az := 10.0
	if err := r.moveTo(nil, &az); err != nil {
		t.Fatalf("move: %v", err)
	}
	r.withBus(func(bus *gosts.Bus) error {
		if on, _ := bus.Servo(ro.Azimuth).TorqueEnabled(); !on {
			t.Error("torque off after a move")
		}
		return nil
	})
}

// Near the end of the platform servo's reach (±7.5 turns), a move the short
// way is out of it: gosts would turn the platform a whole turn back instead
// (seen: nearly a full turn from 0° to 1.1°). The count's started afresh
// where it is first, and the move goes the short way.
func TestPlatformRoom(t *testing.T) {
	var port *servosim.Port
	old := newSimPort
	newSimPort = func(ro Roles) *servosim.Port {
		port = newSim(ro)
		return port
	}
	defer func() { newSimPort = old }()
	cfg, _ := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	r := &rig{cfg: cfg, out: func(any) {}}
	if err := r.connect(context.Background(), simPort, 0); err != nil {
		t.Fatal(err)
	}
	defer r.disconnect()
	ro := r.roles()
	port.SetPosition(ro.Azimuth, 7*gosts.StepsPerRev+819) // 7.2 turns round: at 72°
	r.withBus(func(bus *gosts.Bus) error { return bus.Servo(ro.Azimuth).EnableTorque(true) }) // (holding there, as in a capture)
	if err := r.waitStill(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _ := r.platformTurns()
	az := wrap180(72 + 170) // 170° on, the short way: past 7.5 turns
	if err := r.moveTo(nil, &az); err != nil {
		t.Fatal(err)
	}
	if err := r.waitStill(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := r.platformTurns()
	_, a, _ := r.angles()
	if math.Abs(wrap180(a-az)) > 2 {
		t.Fatalf("at %.1f°, want %.1f°", a, az)
	}
	if math.Abs(after) > 1.5 { // (started afresh: not 6.5 turns, a whole turn back from 7.45)
		t.Fatalf("the platform is %.2f turns round after (%.2f before): it went the long way", after, before)
	}
	t.Logf("before %.2f turns, after %.2f", before, after)
	_ = ro
}
