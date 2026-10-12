package servo

import (
	"path/filepath"
	"testing"

	"github.com/frifox/gosts"
	"github.com/frifox/gosts/cmd/gosts-ctl/internal"
	servosim "github.com/frifox/gosts/cmd/gosts-ctl/servo-sim"
)

// A group near 0° whose members' turn counts are a turn apart (one a step
// below the 0/360° seam when it powered up, the other above): an angle move
// sends each to the angle in its own count, not the leader's goal to both
// (which sent the other a whole turn round, the arm with it).
func TestGroupMoveOwnTurns(t *testing.T) {
	cfg, _, err := internal.LoadConfig(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	port := servosim.NewPort(1, 2)
	bus, _ := gosts.NewBus(port)
	for _, id := range []uint8{1, 2} {
		port.SetPosition(id, 4086) // −0.9°, both
		if err := bus.Servo(id).SetMultiTurn(true); err != nil {
			t.Fatal(err)
		}
	}
	// Holding there: the leader counts it −10, the other 4086 (a turn up).
	if err := bus.Servo(1).MoveTo(-10, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := bus.Servo(2).MoveTo(4086, 0, 0); err != nil {
		t.Fatal(err)
	}
	c := New(cfg, nopNotifier{})
	for _, tc := range []struct {
		deg        float64
		lead, other int
	}{{45, 512, 4096 + 512}, {315, -512, 4096 - 512}} {
		goal, err := c.Move(bus, internal.Request{Type: "angle", Degrees: tc.deg}, []uint8{1, 2})
		if err != nil {
			t.Fatal(err)
		}
		g1, _ := bus.Servo(1).Goal()
		g2, _ := bus.Servo(2).Goal()
		if goal != tc.lead || g1 != tc.lead || g2 != tc.other {
			t.Fatalf("%v°: goal %d, servo 1 → %d, servo 2 → %d; want %d and %d", tc.deg, goal, g1, g2, tc.lead, tc.other)
		}
		// Back to where they were (each in its count) for the next.
		bus.Servo(1).MoveTo(-10, 0, 0)
		bus.Servo(2).MoveTo(4086, 0, 0)
	}
}
