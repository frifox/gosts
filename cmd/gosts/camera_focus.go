package main

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// focusCamera is a camera whose focus can be set from here (Camera
// Settings' Focus tool): focusing once, and driving the lens by hand.
type focusCamera interface {
	// Autofocus focuses once, where the camera's focus area says; in Manual
	// focus mode it then goes back to Manual, so the focus stays (is locked)
	// for the photos. It says how it went (e.g. "Focus Locked").
	Autofocus() (string, error)
	// Nudge drives the lens: steps −7…7, towards near (−) or far (+), a
	// bigger step the bigger the number.
	Nudge(steps int) error
}

// focusModes are the focus modes the Focus tool works in: focusing by hand
// (Manual), or once with a touch up by hand (DMF), or once (AF-S, which
// gphoto2 calls Automatic). AF-C and AF-A keep refocusing on their own.
var focusModes = []string{"Manual", "DMF", "Automatic"}

// afWait is how long autofocus gets to find focus.
var afWait = 4 * time.Second

func checkNudge(steps int) error {
	if steps == 0 || steps < -7 || steps > 7 {
		return fmt.Errorf("focus steps must be −7…7, not 0")
	}
	return nil
}

// ---------------------------------------------------------------- gphoto2

// Autofocus half-presses the shutter (gphoto2's autofocus action) till the
// camera says focus is locked, or not to be found. It needs an autofocus
// mode: in Manual it switches to AF-S for it, and back after (the lens stays
// where autofocus left it).
func (g *gphoto2Camera) Autofocus() (string, error) {
	path := gphoto2Settings["focus"]
	get := func(p string) (string, error) {
		outs, err := g.runLines([]string{"get-config " + p})
		if err != nil {
			return "", err
		}
		c, err := parseGphoto2Config(outs[0])
		return c.current, err
	}
	mode, err := get(path)
	if err != nil {
		return "", err
	}
	manual := mode == "Manual"
	if manual {
		if err := g.Set("focus", "Automatic"); err != nil {
			return "", fmt.Errorf("switching to AF-S to focus: %w", err)
		}
	}
	if _, err := g.runLines([]string{"set-config /main/actions/autofocus=1"}); err != nil {
		return "", err
	}
	ind, err := g.awaitFocus()
	g.runLines([]string{"set-config /main/actions/autofocus=0"}) // let go of the half-press
	if err != nil {
		return "", err
	}
	if manual {
		// Back to Manual, the lens staying where it focused. The camera won't
		// change focus mode while it still says it's locked: a few seconds
		// after letting go it says Unlock, and takes it.
		var lerr error
		for try := 0; try < 3; try++ {
			for deadline := time.Now().Add(afWait); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
				if v, err := get("/main/status/focusindication"); err != nil || v == "Unlock" {
					break
				}
			}
			if lerr = g.Set("focus", "Manual"); lerr == nil {
				break
			}
		}
		if lerr != nil {
			return ind, fmt.Errorf("focused (%s), but the camera stayed in AF-S, not back to Manual: %w", ind, lerr)
		}
	}
	return ind, nil
}

// awaitFocus waits for the half-pressed camera to find focus (or give up),
// and says how it went.
func (g *gphoto2Camera) awaitFocus() (string, error) {
	get := func() (string, error) {
		outs, err := g.runLines([]string{"get-config /main/status/focusindication"})
		if err != nil {
			return "", err
		}
		c, err := parseGphoto2Config(outs[0])
		return c.current, err
	}
	ind := ""
	var err error
	for deadline := time.Now().Add(afWait); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		if ind, err = get(); err != nil {
			return "", err
		}
		if ind != "" && ind != "Unlock" && !strings.HasPrefix(ind, "Tracking") {
			break // found (Focus Locked) or given up (No Focus…)
		}
	}
	if ind == "" || ind == "Unlock" {
		ind = "No focus found"
	}
	return ind, nil
}

// Nudge drives the lens (gphoto2's manualfocus action: Sony's near/far
// steps). It works in Manual and DMF focus modes.
func (g *gphoto2Camera) Nudge(steps int) error {
	if err := checkNudge(steps); err != nil {
		return err
	}
	outs, err := g.runLines([]string{fmt.Sprintf("set-config /main/actions/manualfocus=%d", steps)})
	if err != nil {
		return err
	}
	if strings.Contains(outs[0], "*** Error") {
		return fmt.Errorf("the camera wouldn't drive the focus: %s", gphoto2Error([]byte(outs[0])))
	}
	time.Sleep(nudgeSettle) // the lens moving
	return nil
}

// nudgeSettle is how long the lens gets to move after a nudge.
var nudgeSettle = 300 * time.Millisecond

// ---------------------------------------------------------------- simulator

// The simulated lens: focus is a position, sharp at 0; it starts off
// (simFocusStart), autofocus puts it at 0, a nudge moves it by its steps
// (bigger steps further: 1, 2, 4… as Sony's). Its photos blur with it.
const simFocusStart = 9

func (s *simCamera) Autofocus() (string, error) {
	time.Sleep(600 * time.Millisecond) // finding it
	s.mu.Lock()
	defer s.mu.Unlock()
	s.focus = 0
	s.focusSet = true
	return "Focus Locked", nil
}

func (s *simCamera) Nudge(steps int) error {
	if err := checkNudge(steps); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.focusSet {
		s.focus, s.focusSet = simFocusStart, true
	}
	d := 1 << (abs(steps) - 1) // 1, 2, 4… 64
	if steps < 0 {
		d = -d
	}
	s.focus += d
	return nil
}

// blur is how blurred (px, at the simulated photo's 1200 wide) its photos
// come out: by how far the focus is off.
func (s *simCamera) blur() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.focus
	if !s.focusSet {
		f = simFocusStart
	}
	return math.Min(12, math.Abs(float64(f))*0.35)
}
