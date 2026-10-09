package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/frifox/gosts"
	servosim "github.com/frifox/gosts/cmd/gosts-ctl/servo-sim"
	"go.bug.st/serial/enumerator"
)

// simPort selects the simulated board (servos 1, 2 and 3).
const simPort = "sim"

// rig is the connection to the driver board and the rig's three servos.
type rig struct {
	cfg *configFile
	out func(msg any) // broadcast to every window

	busMu sync.RWMutex
	bus   *gosts.Bus

	// attach, detach: the servo console (gosts-ctl's, in the page's Servo Ctl)
	// working on the board too, given a view of the bus once the servos are
	// found, let go of before the bus closes (nil: none).
	attach func(bus *gosts.Bus, port string, baud int, ids []uint8)
	detach func()

	mu       sync.Mutex
	port     string
	baud     int
	found    []uint8 // servos found by the last scan
	scanning bool
	target   struct {
		elevation, azimuth float64
		set                bool
	}
	elevation, azimuth float64 // last measured
}

// where returns the last measured elevation and azimuth.
func (r *rig) where() (float64, float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.elevation, r.azimuth
}

// withBus runs f with the connected bus, or fails if there is none.
func (r *rig) withBus(f func(*gosts.Bus) error) error {
	r.busMu.RLock()
	defer r.busMu.RUnlock()
	if r.bus == nil {
		return errors.New("no driver board connected")
	}
	return f(r.bus)
}

// connect opens port (simPort for the simulator), applies the mirroring of
// the roles and scans for servos.
func (r *rig) connect(ctx context.Context, port string, baud int) error {
	if port == "" {
		return errors.New("no port selected")
	}
	r.disconnect()
	if baud <= 0 {
		baud = gosts.DefaultBaudRate
	}
	var bus *gosts.Bus
	var err error
	if port == simPort {
		bus, err = gosts.NewBus(newSimPort(r.cfg.get().Roles))
	} else {
		bus, err = gosts.Open(port, baud)
	}
	if err != nil {
		return err
	}
	r.applyRoles(bus, r.cfg.get().Roles)
	r.busMu.Lock()
	r.bus = bus
	r.busMu.Unlock()
	r.mu.Lock()
	r.port, r.baud, r.found = port, baud, nil
	r.mu.Unlock()
	r.logf("info", "connected to %s", port)
	return r.scan(ctx)
}

// Simulator start: the camera at elevation 20°, the platform at azimuth 0°.
const simElevation, simAzimuth = 20, 0

// newSimPort makes the simulated board (tests replace it to start the
// servos elsewhere).
var newSimPort = newSim

// newSim makes a simulated board with the role servos at the simulator's
// start position (mirroring and inversion applied, as on the real rig).
func newSim(ro Roles) *servosim.Port {
	ids := []uint8{1, 2, 3}
	for _, id := range []uint8{ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth} {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	p := servosim.NewPort(ids...)
	physical := func(deg float64, invert, mirrored bool) int {
		s := stepsFor(deg, invert) // logical
		if mirrored {
			s = 2*gosts.CenterPosition - s
		}
		return (s%gosts.StepsPerRev + gosts.StepsPerRev) % gosts.StepsPerRev
	}
	p.SetPosition(ro.ElevationLeader, physical(simElevation, ro.InvertElevation, ro.LeaderMirrored))
	p.SetPosition(ro.ElevationFollower, physical(simElevation, ro.InvertElevation, ro.FollowerMirrored))
	p.SetPosition(ro.Azimuth, physical(simAzimuth, ro.InvertAzimuth, false))
	return p
}

func (r *rig) disconnect() {
	if r.detach != nil {
		r.detach()
	}
	r.busMu.Lock()
	if r.bus != nil {
		r.bus.Close()
		r.bus = nil
	}
	r.busMu.Unlock()
	r.mu.Lock()
	r.port, r.found, r.target.set = "", nil, false
	r.mu.Unlock()
	r.sendState()
}

func (r *rig) applyRoles(bus *gosts.Bus, ro Roles) {
	bus.SetMirrored(ro.ElevationLeader, ro.LeaderMirrored)
	bus.SetMirrored(ro.ElevationFollower, ro.FollowerMirrored)
}

// setRoles saves new roles and applies their mirroring.
func (r *rig) setRoles(ro Roles) error {
	ids := []uint8{ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth}
	if ids[0] == ids[1] || ids[0] == ids[2] || ids[1] == ids[2] {
		return errors.New("each role needs its own servo")
	}
	old := r.cfg.get().Roles
	if err := r.cfg.update(func(c *Config) { c.Roles = ro }); err != nil {
		return err
	}
	r.withBus(func(bus *gosts.Bus) error {
		for _, id := range []uint8{old.ElevationLeader, old.ElevationFollower} {
			bus.SetMirrored(id, false)
		}
		r.applyRoles(bus, ro)
		return nil
	})
	r.logf("info", "roles: elevation #%d (leader) + #%d, azimuth #%d", ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth)
	r.mu.Lock()
	found := slices.Clone(r.found)
	r.mu.Unlock()
	r.ensureMultiTurn(found) // the newly assigned servos too
	r.sendState()
	return nil
}

// rolesFromGroups assigns the roles from the servo console's groups (members
// leader first) when that's clear: three servos found and one group of two of
// them, which is the elevation (its mirroring the console's), the third the
// azimuth. Otherwise the roles are as they are (Setup assigns them).
func (r *rig) rolesFromGroups(groups [][]uint8, mirrored func(uint8) bool) {
	r.mu.Lock()
	found := slices.Clone(r.found)
	r.mu.Unlock()
	if len(found) != 3 {
		return
	}
	var pair []uint8
	for _, g := range groups {
		if len(g) == 2 && slices.Contains(found, g[0]) && slices.Contains(found, g[1]) {
			if pair != nil {
				return // two: which one?
			}
			pair = g
		}
	}
	if pair == nil {
		return
	}
	ro := r.cfg.get().Roles
	old := ro
	ro.ElevationLeader, ro.ElevationFollower = pair[0], pair[1]
	ro.LeaderMirrored, ro.FollowerMirrored = mirrored(pair[0]), mirrored(pair[1])
	for _, id := range found {
		if !slices.Contains(pair, id) {
			ro.Azimuth = id
		}
	}
	if ro == old {
		return
	}
	r.logf("info", "roles from Servo Ctl's group of #%d and #%d: they're the elevation, #%d the azimuth", pair[0], pair[1], ro.Azimuth)
	if err := r.setRoles(ro); err != nil {
		r.logf("error", "roles: %v", err)
	}
}

// scan looks for servos 0..20 (the rig uses low IDs).
func (r *rig) scan(ctx context.Context) error {
	r.mu.Lock()
	if r.scanning {
		r.mu.Unlock()
		return errors.New("a scan is already running")
	}
	r.scanning = true
	r.mu.Unlock()
	r.sendState()
	var found []uint8
	err := r.withBus(func(bus *gosts.Bus) error {
		res, err := bus.ScanRange(ctx, 0, 20, 15*time.Millisecond, nil)
		for _, sr := range res {
			found = append(found, sr.ID)
		}
		return err
	})
	r.mu.Lock()
	r.scanning = false
	if err == nil {
		r.found = found
	}
	port, baud := r.port, r.baud
	r.mu.Unlock()
	if err == nil && r.attach != nil {
		_ = r.withBus(func(bus *gosts.Bus) error {
			r.attach(bus.View(), port, baud, found) // its own mirroring: the console's settings
			return nil
		})
	}
	if err != nil {
		r.logf("error", "scan: %v", err)
	} else {
		r.logf("info", "found servos %v", found)
		r.ensureMultiTurn(found) // always: see ensureMultiTurn
		ro := r.cfg.get().Roles
		for _, id := range []uint8{ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth} {
			if !slices.Contains(found, id) {
				r.logf("error", "servo %d (in the rig's roles) was not found: check its power and cable, or change the roles in Setup", id)
			}
		}
	}
	r.sendState()
	return err
}

// ensureMultiTurn switches the found role servos to multi-turn mode (angle
// limits 0/0, saved on the servo) if they aren't already.
func (r *rig) ensureMultiTurn(found []uint8) {
	ro := r.cfg.get().Roles
	_ = r.withBus(func(bus *gosts.Bus) error {
		for _, id := range []uint8{ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth} {
			if !slices.Contains(found, id) {
				continue
			}
			sv := bus.Servo(id)
			lo, hi, err := sv.AngleLimits()
			if err != nil {
				r.logf("error", "servo %d: %v", id, err)
				continue
			}
			if lo == 0 && hi == 0 {
				continue
			}
			if err := sv.SetMultiTurn(true); err != nil {
				r.logf("error", "servo %d: enabling multi-turn: %v", id, err)
				continue
			}
			r.logf("info", "servo %d: multi-turn enabled, so moves always take the short way round", id)
		}
		return nil
	})
}

// ready checks that the three role servos were found.
func (r *rig) ready() (Roles, error) {
	ro := r.cfg.get().Roles
	r.mu.Lock()
	found := slices.Clone(r.found)
	r.mu.Unlock()
	for _, id := range []uint8{ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth} {
		if !slices.Contains(found, id) {
			return ro, fmt.Errorf("servo %d was not found on the bus", id)
		}
	}
	return ro, nil
}

// Angle conversions. Servo readings are logical (mirroring applied) and
// wrap every turn; angles here are -180..180.

func wrap180(d float64) float64 { return math.Mod(math.Mod(d+180, 360)+360, 360) - 180 }

func elevationOf(pos int, ro Roles) float64 {
	d := wrap180(gosts.StepsToDegrees(pos))
	if ro.InvertElevation {
		d = -d
	}
	return d
}

func azimuthOf(pos int, ro Roles) float64 {
	d := wrap180(gosts.StepsToDegrees(pos))
	if ro.InvertAzimuth {
		d = -d
	}
	return d
}

func stepsFor(deg float64, invert bool) int {
	if invert {
		deg = -deg
	}
	s := gosts.DegreesToSteps(deg)
	return (s%gosts.StepsPerRev + gosts.StepsPerRev) % gosts.StepsPerRev
}

// moveTo starts moves to an elevation and/or azimuth (nil = leave as is),
// the short way round. The elevation is kept within the configured range.
func (r *rig) moveTo(elevation, azimuth *float64) error {
	speed := r.cfg.get().Motion.Speed
	return r.moveToAt(elevation, azimuth, speed, speed)
}

// moveToAt is moveTo with a speed per axis (step/s).
func (r *rig) moveToAt(elevation, azimuth *float64, speedE, speedA int) error {
	ro, err := r.ready()
	if err != nil {
		return err
	}
	c := r.cfg.get()
	return r.withBus(func(bus *gosts.Bus) error {
		acc := uint8(c.Motion.Acc)
		speed := speedE
		if elevation != nil {
			e := math.Max(c.Motion.ElevationMin, math.Min(c.Motion.ElevationMax, *elevation))
			targets, err := elevationTargets(bus, ro, e, speed, acc)
			if err == nil {
				err = bus.SyncMove(targets...)
			}
			if err != nil {
				return fmt.Errorf("elevation: %w", err)
			}
			r.mu.Lock()
			r.target.elevation, r.target.set = e, true
			r.mu.Unlock()
		}
		if azimuth != nil {
			a := wrap180(*azimuth)
			if _, err := bus.Servo(ro.Azimuth).MoveToShortest(stepsFor(a, ro.InvertAzimuth), speedA, acc); err != nil {
				return fmt.Errorf("azimuth: %w", err)
			}
			r.mu.Lock()
			r.target.azimuth, r.target.set = a, true
			r.mu.Unlock()
		}
		return nil
	})
}

// elevationTargets are the two elevation servos' goals for elevation e
// (already within the limits): each servo goes from where it is by the
// difference in angle, straight through the range between, never the other
// way round. Each is worked out in that servo's own turn count: two servos
// a step either side of the 0/4095 seam at 0° are a whole turn apart in
// theirs, so one shared goal would send one of them the long way round,
// past the limits.
func elevationTargets(bus *gosts.Bus, ro Roles, e float64, speed int, acc uint8) ([]gosts.Target, error) {
	var targets []gosts.Target
	for _, id := range []uint8{ro.ElevationLeader, ro.ElevationFollower} {
		abs, err := bus.Servo(id).AbsolutePosition()
		if err != nil {
			return nil, fmt.Errorf("servo %d: %w", id, err)
		}
		// Both angles are within ±90°: their difference never wraps.
		steps := int(math.Round((e - elevationOf(abs, ro)) * stepsPerDegree))
		if ro.InvertElevation {
			steps = -steps
		}
		targets = append(targets, gosts.Target{ID: id, Position: abs + steps, Speed: speed, Acc: acc})
	}
	return targets, nil
}

// angles reads the rig's elevation and azimuth now (not the last telemetry).
func (r *rig) angles() (e, a float64, err error) {
	ro, err := r.ready()
	if err != nil {
		return 0, 0, err
	}
	err = r.withBus(func(bus *gosts.Bus) error {
		res, err := bus.SyncFeedback(ro.ElevationLeader, ro.Azimuth)
		if err != nil {
			return err
		}
		fe, fa := res[ro.ElevationLeader], res[ro.Azimuth]
		if fe.Err != nil || fa.Err != nil {
			return errors.Join(fe.Err, fa.Err)
		}
		e, a = elevationOf(fe.Position, ro), azimuthOf(fa.Position, ro)
		return nil
	})
	return e, a, err
}

// platformTurns is where the platform is, in turns from where its servo
// powered up (logical direction: + counts like azimuth). The servo's
// multi-turn goals reach about ±7.5 turns from there.
func (r *rig) platformTurns() (float64, error) {
	ro, err := r.ready()
	if err != nil {
		return 0, err
	}
	var p float64
	err = r.withBus(func(bus *gosts.Bus) error {
		cur, err := bus.Servo(ro.Azimuth).AbsolutePosition()
		if ro.InvertAzimuth {
			cur = -cur
		}
		p = float64(cur) / gosts.StepsPerRev
		return err
	})
	return p, err
}

// resetPlatformTurns starts the platform servo's turn count afresh where
// it is, without moving it (well under a second): its multi-turn goals
// reach only about ±7.5 turns from where the count started, so a long
// moving-shots spiral resets it between laps. The servo's calibrate-middle
// (its current position made to read 2048) re-references the count; then
// its own zero (PositionOffset, which that changed) is put back, so the
// angle reads as before, and its goal set to where it is. Switching it to
// single-turn and back doesn't reset the count (tried on the rig). Checked:
// if the platform's angle moved or the count isn't within a turn after, it
// says so (see unwindPlatform for the slow way).
func (r *rig) resetPlatformTurns() error {
	ro, err := r.ready()
	if err != nil {
		return err
	}
	return r.withBus(func(bus *gosts.Bus) error {
		sv := bus.Servo(ro.Azimuth)
		zero, err := sv.Zero()
		if err != nil {
			return err
		}
		before, err := sv.Position()
		if err != nil {
			return err
		}
		calErr := sv.CalibrateMiddle()
		if err := sv.SetZero(zero); err != nil { // the zero back, whatever happened
			return errors.Join(calErr, fmt.Errorf("putting the platform servo's zero back (%d): %w", zero, err))
		}
		if calErr != nil {
			return calErr
		}
		now, err := sv.Position()
		if err != nil {
			return err
		}
		if err := sv.SetGoal(now); err != nil { // hold it there
			return err
		}
		if z, err := sv.Zero(); err != nil || z != zero {
			return fmt.Errorf("the platform servo's zero is %d after the reset, not %d (%v)", z, zero, err)
		}
		if d := gosts.CircularDiff(now, before); d < -3 || d > 3 {
			return fmt.Errorf("the platform moved %d steps in the reset", d)
		}
		if at, err := sv.AbsolutePosition(); err != nil || at < -gosts.StepsPerRev || at > 2*gosts.StepsPerRev {
			return fmt.Errorf("the platform servo's count is %d after the reset (%v)", at, err)
		}
		return nil
	})
}

// unwindPlatform turns the platform whole turns (logical: + like azimuth),
// back to the same angle: its servo's multi-turn goals reach only about
// ±7.5 turns from where it powered up (a power cycle is the only reset:
// switching it to single-turn and back doesn't), so a long moving-shots
// spiral unwinds it between laps. Briskly (unwindSpeed), at the configured
// acceleration; it returns once the platform is there.
func (r *rig) unwindPlatform(ctx context.Context, turns int) error {
	ro, err := r.ready()
	if err != nil {
		return err
	}
	acc := uint8(r.cfg.get().Motion.Acc)
	raw := turns
	if ro.InvertAzimuth {
		raw = -raw
	}
	var goal int
	if err := r.withBus(func(bus *gosts.Bus) error {
		sv := bus.Servo(ro.Azimuth)
		abs, err := sv.AbsolutePosition()
		if err != nil {
			return err
		}
		goal = abs + raw*gosts.StepsPerRev
		return sv.MoveTo(goal, unwindSpeed, acc)
	}); err != nil {
		return err
	}
	// Till it's there: the turns at unwindSpeed, with time to speed up and
	// slow down, and some to spare.
	deadline := time.Now().Add(time.Duration(float64(abs(turns)*gosts.StepsPerRev)/unwindSpeed*float64(time.Second)) + 20*time.Second)
	for {
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), r.stop())
		case <-time.After(200 * time.Millisecond):
		}
		var at int
		if err := r.withBus(func(bus *gosts.Bus) error {
			var err error
			at, err = bus.Servo(ro.Azimuth).AbsolutePosition()
			return err
		}); err != nil {
			return err
		}
		if abs(at-goal) <= 20 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("unwinding the platform: it's at %d, not %d", at, goal)
		}
	}
}

// unwindSpeed is how fast (steps/s) the platform unwinds: about 176°/s.
const unwindSpeed = 2000

// home starts the rig back to 0°/0° after a capture, the short way round.
func (r *rig) home() error {
	zero, zeroA := 0.0, 0.0
	return r.moveTo(&zero, &zeroA)
}

// waitStill waits until the role servos have stopped moving (or ctx ends).
// A heavy arm may stop a little short of its goal, so "stopped" rather than
// "on target" is what counts; the shot records where it really is.
func (r *rig) waitStill(ctx context.Context) error {
	ro, err := r.ready()
	if err != nil {
		return err
	}
	ids := []uint8{ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth}
	still := 0
	deadline := time.Now().Add(20 * time.Second)
	for still < 3 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return errors.New("the rig didn't stop moving within 20 s")
		}
		moving := false
		err := r.withBus(func(bus *gosts.Bus) error {
			res, err := bus.SyncFeedback(ids...)
			for _, f := range res {
				if f.Err == nil && (f.Moving || abs(f.Speed) > 30) {
					moving = true
				}
			}
			return err
		})
		if err != nil {
			return err
		}
		if moving {
			still = 0
		} else {
			still++
		}
	}
	return nil
}

func (r *rig) stop() error {
	ro, err := r.ready()
	if err != nil {
		return err
	}
	return r.withBus(func(bus *gosts.Bus) error {
		return errors.Join(bus.Group(ro.ElevationLeader, ro.ElevationFollower).Stop(), bus.Servo(ro.Azimuth).Stop())
	})
}

func (r *rig) torque(on bool) error {
	ro, err := r.ready()
	if err != nil {
		return err
	}
	ids := []uint8{ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth}
	if !on {
		return r.withBus(func(bus *gosts.Bus) error { return bus.SyncTorque(false, ids...) })
	}
	return r.withBus(func(bus *gosts.Bus) error { return holdChecked(bus, ids) })
}

// Torque on, checked: holdTorque is the torque limit (%) the servos start
// holding with, and after holdSettle each must still be within holdDrift
// steps of where it was and not straining (load under holdStrain of the
// limit); else torque goes off again. A servo told to go somewhere else (a
// goal a turn off, say) would pull away or fight the frame: caught at a
// limited torque, before it can do harm.
const (
	holdTorque = 35.0
	holdSettle = 400 * time.Millisecond
	holdDrift  = 60
	holdStrain = 0.8
)

// holdChecked switches torque on, holding where they are (see Servo.Hold),
// for the servos not holding already, and checks they really just hold.
func holdChecked(bus *gosts.Bus, ids []uint8) error {
	before, err := bus.SyncFeedback(ids...)
	if err != nil {
		return err
	}
	var started []uint8
	for _, id := range ids {
		sv := bus.Servo(id)
		if on, err := sv.TorqueEnabled(); err != nil || on {
			continue // holding already (or no answer: the check below says)
		}
		if err := sv.SetTorqueLimit(holdTorque); err != nil {
			return fmt.Errorf("servo %d: %w", id, err)
		}
		if err := sv.Hold(); err != nil {
			bus.SyncTorque(false, ids...)
			return fmt.Errorf("servo %d: %w", id, err)
		}
		started = append(started, id)
	}
	if len(started) == 0 {
		return nil
	}
	time.Sleep(holdSettle)
	after, err := bus.SyncFeedback(started...)
	var bad []string
	for _, id := range started {
		b, a := before[id], after[id]
		if err != nil || a.Err != nil || b.Err != nil {
			bad = append(bad, fmt.Sprintf("#%d didn't answer", id))
			continue
		}
		if d := gosts.CircularDiff(a.Position, b.Position); abs(d) > holdDrift {
			bad = append(bad, fmt.Sprintf("#%d moved %d steps", id, d))
		} else if math.Abs(a.Load) >= holdStrain*holdTorque {
			bad = append(bad, fmt.Sprintf("#%d strained (load %.0f%%)", id, math.Abs(a.Load)))
		}
	}
	if len(bad) > 0 {
		bus.SyncTorque(false, ids...)
		for _, id := range started {
			bus.Servo(id).SetTorqueLimit(100)
		}
		return fmt.Errorf("torque switched off again: instead of holding where it was, %s", strings.Join(bad, ", "))
	}
	for _, id := range started {
		if err := bus.Servo(id).SetTorqueLimit(100); err != nil {
			return fmt.Errorf("servo %d: %w", id, err)
		}
	}
	return nil
}

// telemetry is one live frame for the browser.
type telemetry struct {
	Type      string                 `json:"type"` // "telemetry"
	Time      int64                  `json:"time"`
	Elevation *float64               `json:"elevation"` // leader's angle; nil if unknown
	Azimuth   *float64               `json:"azimuth"`
	Spread    float64                `json:"spread"` // degrees between the two elevation servos
	Target    *[2]float64            `json:"target,omitempty"`
	Servos    map[string]servoStatus `json:"servos"`
}

type servoStatus struct {
	Role   string  `json:"role"`
	Pos    int     `json:"pos"`
	Moving bool    `json:"moving"`
	Load   float64 `json:"load"`
	Volt   float64 `json:"volt"`
	Temp   int     `json:"temp"`
	Torque bool    `json:"torque"`
	Status string  `json:"status"`
	Error  string  `json:"error,omitempty"`
}

// pollLoop streams telemetry while connected.
func (r *rig) pollLoop(ctx context.Context) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	n := 0
	torque := map[uint8]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ro := r.cfg.get().Roles
		r.mu.Lock()
		connected, scanning := r.port != "", r.scanning
		tg := r.target
		r.mu.Unlock()
		if !connected || scanning {
			continue
		}
		roles := map[uint8]string{ro.ElevationLeader: "elevation (leader)", ro.ElevationFollower: "elevation", ro.Azimuth: "azimuth"}
		ids := []uint8{ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth}
		msg := telemetry{Type: "telemetry", Time: time.Now().UnixMilli(), Servos: map[string]servoStatus{}}
		_ = r.withBus(func(bus *gosts.Bus) error {
			res, _ := bus.SyncFeedback(ids...)
			if n%10 == 0 { // torque switch once a second: not part of the feedback
				for _, id := range ids {
					if on, err := bus.Servo(id).TorqueEnabled(); err == nil {
						torque[id] = on
					}
				}
			}
			for _, id := range ids {
				f, ok := res[id]
				st := servoStatus{Role: roles[id], Torque: torque[id]}
				switch {
				case !ok:
					st.Error = "no reply"
				case f.Err != nil:
					st.Error = f.Err.Error()
				default:
					st.Pos, st.Moving, st.Load, st.Volt, st.Temp, st.Status = f.Position, f.Moving, f.Load, f.Voltage, f.Temperature, f.Status.String()
				}
				msg.Servos[fmt.Sprint(id)] = st
			}
			if f, ok := res[ro.ElevationLeader]; ok && f.Err == nil {
				e := elevationOf(f.Position, ro)
				msg.Elevation = &e
				if g, ok := res[ro.ElevationFollower]; ok && g.Err == nil {
					msg.Spread = math.Abs(gosts.StepsToDegrees(gosts.CircularDiff(f.Position, g.Position)))
				}
			}
			if f, ok := res[ro.Azimuth]; ok && f.Err == nil {
				a := azimuthOf(f.Position, ro)
				msg.Azimuth = &a
			}
			return nil
		})
		if tg.set {
			msg.Target = &[2]float64{tg.elevation, tg.azimuth}
		}
		r.mu.Lock()
		if msg.Elevation != nil {
			r.elevation = *msg.Elevation
		}
		if msg.Azimuth != nil {
			r.azimuth = *msg.Azimuth
		}
		r.mu.Unlock()
		n++
		r.out(msg)
	}
}

// Messages for the board side.

type stateMsg struct {
	Type      string `json:"type"` // "state"
	Connected bool   `json:"connected"`
	Port      string `json:"port"`
	Scanning  bool   `json:"scanning"`
	Found     []int  `json:"found"`
	Config    Config `json:"config"`
}

func (r *rig) state() stateMsg {
	r.mu.Lock()
	defer r.mu.Unlock()
	found := []int{}
	for _, id := range r.found {
		found = append(found, int(id))
	}
	return stateMsg{Type: "state", Connected: r.port != "", Port: r.port, Scanning: r.scanning, Found: found, Config: r.cfg.get()}
}

func (r *rig) sendState() { r.out(r.state()) }

type logMsg struct {
	Type    string `json:"type"` // "log"
	Level   string `json:"level"`
	Message string `json:"message"`
}

func (r *rig) logf(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	logPrint(msg)
	r.out(logMsg{Type: "log", Level: level, Message: msg})
}

type portInfo struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	Likely bool   `json:"likely"` // a USB-UART bridge like the Bus Servo Adapter's
}

// listPorts lists serial ports, likely adapters first.
func listPorts() ([]portInfo, error) {
	ds, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, err
	}
	bridges := map[string]string{"1A86": "WCH CH34x", "0403": "FTDI", "10C4": "CP210x", "067B": "PL2303"}
	out := []portInfo{}
	for _, d := range ds {
		if runtime.GOOS == "darwin" && strings.HasPrefix(d.Name, "/dev/tty.") {
			continue // use the cu device
		}
		p := portInfo{Name: d.Name}
		if b, ok := bridges[strings.ToUpper(d.VID)]; ok && d.IsUSB {
			p.Detail, p.Likely = b, true
		} else if d.IsUSB {
			p.Detail = "USB " + d.VID + ":" + d.PID
		}
		out = append(out, p)
	}
	slices.SortStableFunc(out, func(a, b portInfo) int {
		if a.Likely == b.Likely {
			return 0
		}
		if a.Likely {
			return -1
		}
		return 1
	})
	return out, nil
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// diag reads the role servos' raw registers (no writes), for debugging:
// what the servo itself holds, before mirroring.
func (r *rig) diag() (map[string]map[string]int, error) {
	ro := r.cfg.get().Roles
	out := map[string]map[string]int{}
	err := r.withBus(func(bus *gosts.Bus) error {
		for _, id := range []uint8{ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth} {
			m := map[string]int{}
			sv := bus.Servo(id)
			for _, reg := range []gosts.Register{gosts.RegPresentPosition, gosts.RegGoalPosition, gosts.RegPositionOffset,
				gosts.RegMode, gosts.RegMinAngleLimit, gosts.RegMaxAngleLimit, gosts.RegTorqueEnable, gosts.RegTorqueLimit,
				gosts.RegStatus, gosts.RegPresentLoad, gosts.RegPositionP, gosts.RegPositionD, gosts.RegPositionI} {
				v, err := sv.Read(reg)
				if err != nil {
					m[reg.Name+"Err"] = 1
					continue
				}
				m[reg.Name] = v
			}
			if bus.Mirrored(id) {
				m["Mirrored"] = 1
			}
			out[fmt.Sprint(id)] = m
		}
		return nil
	})
	return out, err
}
