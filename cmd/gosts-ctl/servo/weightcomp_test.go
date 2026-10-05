package servo

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/frifox/gosts"
	"github.com/frifox/gosts/cmd/gosts-ctl/internal"
	servosim "github.com/frifox/gosts/cmd/gosts-ctl/servo-sim"
)

func TestWeightComp(t *testing.T) {
	cfg, _, _ := internal.LoadConfig(filepath.Join(t.TempDir(), "config.toml"))
	bus, _ := gosts.NewBus(servosim.NewPort(1))
	c := New(cfg, nopNotifier{})
	sv := bus.Servo(1)
	if err := sv.MoveTo(2000, 0, 0); err != nil { // torque on, target 2000
		t.Fatal(err)
	}
	if err := c.SetWeightComp(bus, []uint8{1}, true); err != nil {
		t.Fatal(err)
	}
	never := func(uint8) bool { return false }
	// The arm hangs 20 steps short of whatever goal it has; run frames
	// until the correction settles.
	frame := func(pos int) {
		states := map[string]internal.ServoState{strconv.Itoa(1): {Feedback: gosts.Feedback{Position: pos}}}
		c.WeightComp(bus, states, never)
	}
	pos := 1980 // sagging 20 short of the target
	for i := 0; i < 200; i++ {
		g, _ := sv.Goal()
		pos = g - 20 // the arm always sits 20 below its goal
		frame(pos)
	}
	if g, _ := sv.Goal(); g < 2018 || g > 2022 || pos < 1998 {
		t.Fatalf("goal %d, arm %d: want the goal ~20 past the target and the arm at it", g, pos)
	}

	// Turning it off puts the goal back on the target.
	if err := c.SetWeightComp(bus, []uint8{1}, false); err != nil {
		t.Fatal(err)
	}
	if g, _ := sv.Goal(); g != 2000 {
		t.Fatal("goal not restored", g)
	}

	// A large sag is capped.
	c.SetWeightComp(bus, []uint8{1}, true)
	for i := 0; i < 400; i++ {
		frame(1500) // stuck far below
	}
	if g, _ := sv.Goal(); g != 2000+compMaxOffset {
		t.Fatal("not capped", g)
	}

	// A new move becomes the target.
	sv.MoveTo(1000, 0, 0)
	for i := 0; i < 200; i++ {
		g, _ := sv.Goal()
		frame(g - 10)
	}
	if g, _ := sv.Goal(); g < 1008 || g > 1012 {
		t.Fatal("new target not followed", g)
	}

	// Torque off: no goal writes (they would switch it on).
	sv.EnableTorque(false)
	before, _ := sv.Goal()
	for i := 0; i < 50; i++ {
		frame(900)
	}
	if g, _ := sv.Goal(); g != before {
		t.Fatal("wrote a goal with torque off", g)
	}
	if on, _ := sv.TorqueEnabled(); on {
		t.Fatal("torque switched on")
	}
}
