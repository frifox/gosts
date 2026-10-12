package servo

import (
	"fmt"
	"math"

	"github.com/frifox/gosts"
	"github.com/frifox/gosts/cmd/gosts-ctl/internal"
)

// turnWindow keeps angle moves within one turn either side of the center
// (multi-turn mode), so repeated moves can't wind up a cable.
var turnWindow = [2]int{gosts.CenterPosition - gosts.StepsPerRev, gosts.CenterPosition + gosts.StepsPerRev}

// Move runs a move whose goal depends on the servo's state, read fresh here:
// "angle" goes to req.Degrees (as the console shows angles) the short way
// round (multi-turn mode), "jog" moves req.Position steps from the current
// target (see jogBase). ids are the servos to move, leader first (one servo, or a
// group's members); the leader decides the goal. It returns the goal.
//
// Both use the servo's absolute position: in multi-turn mode the reported
// position wraps every turn, but goals use the servo's turn count.
func (c *Controller) Move(bus *gosts.Bus, req internal.Request, ids []uint8) (int, error) {
	lead := bus.Servo(ids[0])
	var goal int
	var err error
	if req.Type == "jog" {
		var base int
		if base, err = c.jogBase(lead); err != nil {
			return 0, err
		}
		goal = base + req.Position
	} else {
		// req.Degrees is the angle as the console shows it: the servo's
		// reading.
		reading := int(math.Round(req.Degrees / gosts.DegreesPerStep))
		if goal, err = lead.ShortestGoal(internal.WrapSteps(reading), turnWindow[0], turnWindow[1]); err != nil {
			return 0, err
		}
	}
	if len(ids) > 1 {
		return goal, moveGroup(bus, ids, goal, req.Speed, req.Acc)
	}
	return goal, lead.MoveTo(goal, req.Speed, req.Acc)
}

// moveGroup moves a group's members (leader first) to goal, the leader's,
// each in its own turn count. In multi-turn mode each servo counts turns
// from where it powered up: two members a step either side of the 0/360°
// seam are a whole turn apart in their counts, so the leader's goal as it is
// would send the other a whole turn round (and the arm with it). Each other
// member's goal is the leader's shifted by the whole turns between their
// counts. (Single-turn: no turns, the goal as it is.)
func moveGroup(bus *gosts.Bus, ids []uint8, goal, speed int, acc uint8) error {
	targets, err := groupTargets(bus, ids, goal, speed, acc)
	if err != nil {
		return err
	}
	return bus.SyncMove(targets...)
}

func groupTargets(bus *gosts.Bus, ids []uint8, goal, speed int, acc uint8) ([]gosts.Target, error) {
	targets := []gosts.Target{{ID: ids[0], Position: goal, Speed: speed, Acc: acc}}
	if len(ids) < 2 {
		return targets, nil
	}
	lead := bus.Servo(ids[0])
	lo, hi, err := lead.AngleLimits()
	if err != nil {
		return nil, err
	}
	multi := lo == 0 && hi == 0
	var leadAt int
	if multi {
		if leadAt, err = lead.AbsolutePosition(); err != nil {
			return nil, fmt.Errorf("servo %d: %w", ids[0], err)
		}
	}
	for _, id := range ids[1:] {
		g := goal
		if multi {
			at, err := bus.Servo(id).AbsolutePosition()
			if err != nil {
				return nil, fmt.Errorf("servo %d: %w", id, err)
			}
			g += turnsApart(at, leadAt) * gosts.StepsPerRev
		}
		targets = append(targets, gosts.Target{ID: id, Position: g, Speed: speed, Acc: acc})
	}
	return targets, nil
}

// turnsApart is how many whole turns a member's count is from the leader's,
// the two (about) at the same angle.
func turnsApart(member, leader int) int {
	return int(math.Round(float64(member-leader) / gosts.StepsPerRev))
}

// jogBase is where a jog counts from: the current target (the goal, or with
// weight compensation the target behind the corrected goal), so repeated
// jogs add up exactly however far the arm sags or lags. With torque off the
// goal may be stale (e.g. 0 after a power dip), so it counts from the arm.
func (c *Controller) jogBase(sv *gosts.Servo) (int, error) {
	on, err := sv.TorqueEnabled()
	if err != nil {
		return 0, err
	}
	if !on {
		return sv.AbsolutePosition()
	}
	goal, err := sv.Goal()
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if st := c.comp[sv.ID()]; st != nil && st.known && st.commanded == goal {
		return st.target, nil
	}
	return goal, nil
}
