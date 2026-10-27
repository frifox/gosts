package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/frifox/gosts"
	servosim "github.com/frifox/gosts/cmd/gosts-ctl/servo-sim"
)

// The elevation pair zeroed where it stands (Servo Settings: Group, Setup,
// Set 0° to Current position: each servo's 0/4095 seam right there), then
// nudged (the swing settling, a hand), Torque Lock on, and moved: each holds
// where it is (no goal a turn away: no spin, no tug of war) and they move
// together, whichever side of the seam each is on, whatever turns their
// counts had before.
func TestSeamCalibrateHoldMove(t *testing.T) {
	cases := []struct {
		nudge  int  // logical steps, after zeroing (across the seam if < 0)
		turns  int  // whole turns already on the follower's count before
		holdOn bool // zeroed while holding (torque on), not let go
		move   bool // moved after (slow: some cases)
	}{
		{-11, 0, false, true}, {11, 0, false, true}, {0, 0, false, false}, {-1, 0, false, false}, {1, 0, false, false},
		{-300, 0, false, false}, {300, 0, false, false}, {-11, 2, false, true}, {11, -3, false, false}, {-11, -1, false, false},
		{-11, 0, true, true}, {11, 1, true, false},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("nudge %+d, turns %+d, zeroed holding %v", c.nudge, c.turns, c.holdOn), func(t *testing.T) {
			seamCase(t, c.nudge, c.turns, c.holdOn, c.move)
		})
	}
}

func seamCase(t *testing.T, nudge, turns int, holdOn, move bool) {
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
	ro := r.roles() // (#1 the leader, mirrored; #2 the follower)
	lead, fol := ro.ElevationLeader, ro.ElevationFollower
	if !ro.LeaderMirrored || ro.FollowerMirrored {
		t.Fatalf("set up: roles %+v", ro)
	}
	if err := r.torque(false, "elevation"); err != nil {
		t.Fatal(err)
	}
	// The frame at some pose (logical 1096 on both: the mirrored one's
	// physical the other way), the follower with turns on its count.
	port.Turn(lead, 4096-1096)
	port.Turn(fol, 1096+turns*4096)
	time.Sleep(50 * time.Millisecond)
	if holdOn { // (holding there first: Torque Lock, then the zero set)
		if err := r.torque(true, "elevation"); err != nil {
			t.Fatalf("Torque Lock before zeroing: %v", err)
		}
	}
	if err := r.withBus(func(bus *gosts.Bus) error { return bus.Group(lead, fol).SetPositionAs(0) }); err != nil {
		t.Fatalf("set 0°: %v", err)
	}
	if holdOn { // (let go, to be nudged)
		if err := r.torque(false, "elevation"); err != nil {
			t.Fatal(err)
		}
	}
	// Nudged (by hand, torque off): the frame moves the two together, the
	// mirrored one's physical position the other way.
	port.Turn(lead, 4096-1096-nudge)
	port.Turn(fol, 1096+turns*4096+nudge)
	time.Sleep(50 * time.Millisecond)
	read := func() (lp, fp int, ll, fl float64) {
		r.withBus(func(bus *gosts.Bus) error {
			res, err := bus.SyncFeedback(lead, fol)
			lp, fp, ll, fl = res[lead].Position, res[fol].Position, res[lead].Load, res[fol].Load
			return err
		})
		return
	}
	want := ((nudge % 4096) + 4096) % 4096
	if lp, fp, _, _ := read(); lp != want || fp != want {
		t.Fatalf("set up: read %d, %d after the nudge, want %d", lp, fp, want)
	}
	if err := r.torque(true, "elevation"); err != nil {
		t.Fatalf("Torque Lock: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	lp, fp, ll, fl := read()
	if d1, d2 := gosts.CircularDiff(lp, want), gosts.CircularDiff(fp, want); abs(d1) > 5 || abs(d2) > 5 || math.Abs(ll) > 30 || math.Abs(fl) > 30 {
		t.Fatalf("held: at %d (%+d), %d (%+d), loads %.0f%%, %.0f%%: want both where they were (%d), easy", lp, d1, fp, d2, ll, fl, want)
	}
	if !move {
		return
	}
	held := map[uint8]int{} // (each one's count, holding: the moves go from it, never a turn off)
	r.withBus(func(bus *gosts.Bus) error {
		for _, id := range []uint8{lead, fol} {
			held[id], _ = bus.Servo(id).AbsolutePosition()
		}
		return nil
	})
	for _, e := range []float64{5, -5, 0} {
		e := e
		if err := r.moveTo(&e, nil); err != nil {
			t.Fatalf("move to %.0f°: %v", e, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := r.waitStill(ctx)
		cancel()
		if err != nil {
			t.Fatalf("move to %.0f°: %v", e, err)
		}
		lp, fp, _, _ := read()
		le, fe := elevationOf(lp, ro), elevationOf(fp, ro)
		if math.Abs(le-e) > 0.6 || math.Abs(fe-e) > 0.6 {
			t.Fatalf("move to %.0f°: at %.1f° and %.1f°", e, le, fe)
		}
		// (each its count: a goal a turn away would show here)
		r.withBus(func(bus *gosts.Bus) error {
			for _, id := range []uint8{lead, fol} {
				at, err := bus.Servo(id).AbsolutePosition()
				// (its count: as held, plus the move from there to e)
				want := held[id] - gosts.CircularDiff(held[id], 0) + int(math.Round(e*stepsPerDegree))
				if err != nil || abs(at-want) > 8 {
					t.Errorf("move to %.0f°: #%d's count at %d (%v), want about %d", e, id, at, err, want)
				}
			}
			return nil
		})
	}
}

// The pair let go near the seam with goals a turn away from their counts
// (stale: seen on hardware after a reconnect, a 4011 goal left from
// earlier): the rig learns both counts by itself, together, without a spin;
// then Torque Lock and moves keep them in step.
func TestSeamPairLearned(t *testing.T) {
	var port *servosim.Port
	old := newSimPort
	newSimPort = func(ro Roles) *servosim.Port { port = newSim(ro); return port }
	defer func() { newSimPort = old }()
	oldRetry := learnRetry
	learnRetry = 300 * time.Millisecond
	defer func() { learnRetry = oldRetry }()
	cfg, _ := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	r := &rig{cfg: cfg, out: func(any) {}}
	if err := r.connect(context.Background(), simPort, 0); err != nil {
		t.Fatal(err)
	}
	defer r.disconnect()
	ro := r.roles()
	lead, fol := ro.ElevationLeader, ro.ElevationFollower
	if err := r.torque(false, "elevation"); err != nil {
		t.Fatal(err)
	}
	port.Turn(lead, 4096-1096)
	port.Turn(fol, 1096)
	time.Sleep(50 * time.Millisecond)
	if err := r.withBus(func(bus *gosts.Bus) error { return bus.Group(lead, fol).SetPositionAs(0) }); err != nil {
		t.Fatal(err)
	}
	// Nudged just short of the seam, and goals a turn away left in them
	// (written raw: that switches torque on, so off again after).
	port.Turn(lead, 4096-1096+11)
	port.Turn(fol, 1096-11)
	if err := r.withBus(func(bus *gosts.Bus) error {
		for _, id := range []uint8{lead, fol} {
			at, err := bus.Servo(id).RestartedPosition() // (its count: a goal a turn from it)
			if err != nil {
				return err
			}
			if err := bus.Servo(id).SetGoal(at + gosts.StepsPerRev); err != nil {
				return err
			}
		}
		return bus.SyncTorque(false, lead, fol)
	}); err != nil {
		t.Fatal(err)
	}
	unknown := func(id uint8) bool {
		var u bool
		r.withBus(func(bus *gosts.Bus) error {
			_, err := bus.Servo(id).AbsolutePosition()
			u = errors.Is(err, gosts.ErrTurnUnknown)
			return nil
		})
		return u
	}
	if !unknown(lead) || !unknown(fol) {
		r.withBus(func(bus *gosts.Bus) error {
			for _, id := range []uint8{lead, fol} {
				sv := bus.Servo(id)
				g, _ := sv.Read(gosts.RegGoalPosition)
				raw, _ := sv.Read(gosts.RegPresentPosition)
				off, _ := sv.Read(gosts.RegPositionOffset)
				at, err := sv.AbsolutePosition()
				rp, _ := sv.RestartedPosition()
				t.Logf("#%d goal %d raw %d off %d abs %d (%v) restarted %d", id, g, raw, off, at, err, rp)
			}
			return nil
		})
		t.Fatal("set up: both counts should be unknown")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.pollLoop(ctx)
	deadline := time.Now().Add(stillFor + 8*time.Second)
	for (unknown(lead) || unknown(fol)) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if unknown(lead) || unknown(fol) {
		t.Fatal("left still, their counts weren't learned")
	}
	time.Sleep(time.Second) // (no learning again and again: once known, it stays so)
	if unknown(lead) || unknown(fol) {
		t.Fatal("known, then unknown again")
	}
	if err := r.torque(true, "elevation"); err != nil {
		t.Fatalf("Torque Lock: %v", err)
	}
	for _, e := range []float64{3, -3} {
		e := e
		if err := r.moveTo(&e, nil); err != nil {
			t.Fatal(err)
		}
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := r.waitStill(c)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		r.withBus(func(bus *gosts.Bus) error {
			res, _ := bus.SyncFeedback(lead, fol)
			if le, fe := elevationOf(res[lead].Position, ro), elevationOf(res[fol].Position, ro); math.Abs(le-e) > 0.6 || math.Abs(fe-e) > 0.6 {
				t.Errorf("move to %.0f°: at %.1f° and %.1f°", e, le, fe)
			}
			return nil
		})
	}
}
