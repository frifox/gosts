package servo

import (
	"fmt"
	"math"
	"strconv"

	"github.com/frifox/gosts"
	"github.com/frifox/gosts/cmd/gosts-ctl/internal"
)

// Weight compensation
//
// A heavy or unbalanced load makes an arm settle slightly short of its goal:
// the servo's position loop needs some error to produce the holding torque.
// With compensation on, once the servo has stopped, its goal is nudged past
// the target by most of the remaining error, repeatedly, until the arm sits
// on the target. The correction is capped (compMaxOffset) so a blocked arm
// can't wind up.
//
// A new move from anywhere (this console, another app) is noticed by the goal
// register no longer holding the last corrected goal: the new goal becomes
// the target.

const (
	compSettle     = 6   // telemetry frames without motion before correcting (~300 ms)
	compEvery      = 4   // then correct every this many frames (~200 ms)
	compTolerance  = 2   // steps: close enough
	compGain       = 0.8 // share of the remaining error added per correction
	compMaxOffset  = 128 // steps (11.25°) the goal may be moved past the target
	compStillSpeed = 30  // step/s: slower than this counts as stopped (reading noise)
)

type compState struct {
	target    int  // the goal the arm should reach (goal coordinates, logical)
	commanded int  // the goal last written (target + correction)
	known     bool // target and commanded are set
	still     int  // consecutive telemetry frames without motion
	capped    bool // the correction reached compMaxOffset (logged once)
}

// WeightComp runs one step of weight compensation for the servos that have
// it on, using a telemetry frame. skip reports servos that must be left alone
// (e.g. being auto-tuned). Called by the poll loop with the bus held.
func (c *Controller) WeightComp(bus *gosts.Bus, states map[string]internal.ServoState, skip func(uint8) bool) {
	for id, sc := range c.cfg.All() {
		if !sc.WeightComp {
			continue
		}
		f, ok := states[strconv.Itoa(int(id))]
		if !ok || f.Error != "" {
			continue
		}
		if skip(id) {
			c.dropComp(id) // learn the goal afresh afterwards
			continue
		}
		if err := c.compStep(bus.Servo(id), f.Feedback); err != nil {
			c.n.Logf("error", "servo %d: weight compensation: %v", id, err)
			c.dropComp(id)
		}
	}
}

func (c *Controller) compStep(sv *gosts.Servo, f gosts.Feedback) error {
	id := sv.ID()
	c.mu.Lock()
	if c.comp == nil {
		c.comp = map[uint8]*compState{}
	}
	st := c.comp[id]
	if st == nil {
		st = &compState{}
		c.comp[id] = st
	}
	if f.Moving || internal.Abs(f.Speed) > compStillSpeed {
		st.still = 0
	} else {
		st.still++
	}
	still := st.still
	c.mu.Unlock()
	if still < compSettle || (still-compSettle)%compEvery != 0 {
		return nil
	}

	goal, err := sv.Goal()
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !st.known || goal != st.commanded {
		// A new move (or the first look): its goal is the target.
		st.target, st.commanded, st.capped, st.known = goal, goal, false, true
	}
	errSteps := gosts.CircularDiff(st.target, f.Position) // how far the arm is short of the target
	if internal.Abs(errSteps) <= compTolerance {
		return nil
	}
	// Writing a goal switches torque on, and only position mode has a goal.
	if on, err := sv.TorqueEnabled(); err != nil || !on {
		return err
	}
	if m, err := sv.Mode(); err != nil || m != gosts.ModePosition {
		return err
	}
	offset := st.commanded - st.target + int(math.Round(compGain*float64(errSteps)))
	if internal.Abs(offset) > compMaxOffset {
		offset = compMaxOffset * sign(offset)
		if !st.capped {
			st.capped = true
			c.n.Logf("error", "servo %d: weight compensation is at its limit (%.1f°) and the arm is still %.1f° off; check the load or the torque limits",
				id, compMaxOffset*gosts.DegreesPerStep, float64(errSteps)*gosts.DegreesPerStep)
		}
	}
	next := st.target + offset
	if next == st.commanded {
		return nil
	}
	if err := sv.SetGoal(next); err != nil {
		return err
	}
	// Read back: a motion range may have kept the goal elsewhere.
	if st.commanded, err = sv.Goal(); err != nil {
		return err
	}
	return nil
}

// SetWeightComp switches weight compensation for ids (saved in config.toml).
// Switching it off puts each servo's goal back on its target.
func (c *Controller) SetWeightComp(bus *gosts.Bus, ids []uint8, on bool) error {
	for _, id := range ids {
		if err := c.cfg.Update(id, func(sc *internal.ServoConfig) { sc.WeightComp = on }); err != nil {
			return err
		}
		if on {
			continue
		}
		c.mu.Lock()
		st := c.comp[id]
		delete(c.comp, id)
		c.mu.Unlock()
		if st == nil || !st.known || st.commanded == st.target {
			continue
		}
		sv := bus.Servo(id)
		goal, err := sv.Goal()
		if err != nil {
			return fmt.Errorf("servo %d: %w", id, err)
		}
		if on, err := sv.TorqueEnabled(); err != nil || !on || goal != st.commanded {
			continue // torque off, or moved elsewhere since: nothing to undo
		}
		if err := sv.SetGoal(st.target); err != nil {
			return fmt.Errorf("servo %d: %w", id, err)
		}
	}
	c.n.BroadcastState()
	c.n.Logf("info", "weight compensation %s for servo(s) %v", internal.OnOff(on), ids)
	return nil
}

func (c *Controller) dropComp(id uint8) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.comp, id)
}

func sign(v int) int {
	if v < 0 {
		return -1
	}
	return 1
}
