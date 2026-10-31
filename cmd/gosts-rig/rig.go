package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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
	// onRoles, if set, is told the roles changed (Servo Settings shows them).
	onRoles func()

	busMu sync.RWMutex
	bus   *gosts.Bus

	// attach, detach: the servo console (gosts-ctl's, in the page's Servo Ctl)
	// working on the board too, given a view of the bus once the servos are
	// found, let go of before the bus closes (nil: none).
	attach func(bus *gosts.Bus, port string, baud int, ids []uint8)
	detach func()
	// ctlFor, if set, readies the servo console for the board about to be
	// connected (sim: the simulator's own settings).
	ctlFor func(sim bool)

	torqueStale atomic.Bool // the torque switches changed: read them on the next poll, not in a second

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
		return errNoBoard
	}
	return f(r.bus)
}

// errNoBoard: no driver board connected (or it's dropped off USB).
var errNoBoard = errors.New("no driver board connected")

// portGone is whether err is the port itself failing, not a servo not
// answering: the driver board off USB (unplugged, or reset: seen when the
// servos' power sags under load, "device not configured"), or no board.
func portGone(err error) bool {
	return err != nil && (errors.Is(err, errNoBoard) || errors.Is(err, syscall.ENXIO) || errors.Is(err, syscall.EIO) ||
		errors.Is(err, syscall.EBADF) || strings.Contains(err.Error(), "device not configured"))
}

// portLost: the driver board dropped off USB: disconnected, and connected
// again once its port's back (a reset: a second or two), for a minute; not
// if it's been connected (or another picked) meanwhile.
func (r *rig) portLost(ctx context.Context) {
	r.mu.Lock()
	port, baud := r.port, r.baud
	r.mu.Unlock()
	if port == "" || port == simPort {
		return
	}
	r.logf("error", "the driver board dropped off USB (%s): its power sagging as the servos pull? (a servo restarting with it loses its turn count) Connecting again once it's back", port)
	r.disconnect()
	go func() {
		for range 60 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			r.mu.Lock()
			idle := r.port == ""
			r.mu.Unlock()
			if !idle {
				return
			}
			if _, err := os.Stat(port); err != nil {
				continue // (not back yet)
			}
			if err := r.connect(ctx, port, baud); err == nil {
				return
			}
		}
		r.logf("error", "the driver board didn't come back on %s: connect it again", port)
	}()
}

// connect opens port (simPort for the simulator), applies the mirroring of
// the roles and scans for servos.
func (r *rig) connect(ctx context.Context, port string, baud int) error {
	if port == "" {
		return errors.New("no port selected")
	}
	r.disconnect()
	if r.ctlFor != nil {
		r.ctlFor(port == simPort)
	}
	if baud <= 0 {
		baud = gosts.DefaultBaudRate
	}
	var bus *gosts.Bus
	var err error
	if port == simPort {
		bus, err = gosts.NewBus(newSimPort(r.rolesFor(port)))
	} else {
		bus, err = gosts.Open(port, baud)
	}
	if err != nil {
		return err
	}
	r.applyRoles(bus, r.rolesFor(port))
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

// simRoles are the simulated rig's roles, whatever the real rig's are (the
// config's): #1 and #2 the elevation (#1 mirrored: they face each other), #3
// the azimuth. Its inversions are the config's.
func simRoles(c Roles) Roles {
	c.ElevationLeader, c.ElevationFollower, c.Azimuth = 1, 2, 3
	c.LeaderMirrored, c.FollowerMirrored = true, false
	return c
}

// rolesFor is the roles of the board on port: the simulator's own, else the
// config's.
func (r *rig) rolesFor(port string) Roles {
	if port == simPort {
		return simRoles(r.cfg.get().Roles)
	}
	return r.cfg.get().Roles
}

// simulated: the simulated board is connected.
func (r *rig) simulated() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.port == simPort
}

// roles is the connected board's roles (see rolesFor).
func (r *rig) roles() Roles {
	r.mu.Lock()
	port := r.port
	r.mu.Unlock()
	return r.rolesFor(port)
}

// setRoles saves new roles and applies their mirroring.
func (r *rig) setRoles(ro Roles) error {
	if r.simulated() {
		return errors.New("the simulator's servos are fixed: #1 and #2 the elevation, #3 the azimuth")
	}
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
	if r.onRoles != nil {
		r.onRoles()
	}
	return nil
}

// assignRole makes ids the rig's role ("elevation": the pair, leader
// first, their mirroring as mirrored says; "azimuth": one): Servo Settings'
// Assign Servo To. Not a servo that has the other role (assign that one
// first).
func (r *rig) assignRole(role string, ids []uint8, mirrored func(uint8) bool) error {
	ro := r.cfg.get().Roles
	switch {
	case role == "elevation" && len(ids) == 2:
		if slices.Contains(ids, ro.Azimuth) {
			return fmt.Errorf("#%d is the azimuth now: assign another servo to Azimuth first", ro.Azimuth)
		}
		ro.ElevationLeader, ro.ElevationFollower = ids[0], ids[1]
		ro.LeaderMirrored, ro.FollowerMirrored = mirrored(ids[0]), mirrored(ids[1])
	case role == "azimuth" && len(ids) == 1:
		if ids[0] == ro.ElevationLeader || ids[0] == ro.ElevationFollower {
			return fmt.Errorf("#%d is in the elevation now: assign another group to Elevation first", ids[0])
		}
		ro.Azimuth = ids[0]
	default:
		return fmt.Errorf("can't assign %d servo(s) to %q", len(ids), role)
	}
	return r.setRoles(ro)
}

// forgetTarget drops the rig's goal (the servos moved some other way: Servo
// Ctl): the page's controls then follow where the rig is.
func (r *rig) forgetTarget() {
	r.mu.Lock()
	r.target.set = false
	r.mu.Unlock()
	r.torqueStale.Store(true) // Servo Ctl may have switched torque too
}

// adoptFound takes the servos another scan of the board found (Servo Ctl's:
// the servos turned on since the rig's own, say) as the rig's.
func (r *rig) adoptFound(ids []uint8) {
	r.mu.Lock()
	if r.port == "" || r.scanning {
		r.mu.Unlock()
		return
	}
	found := slices.Clone(ids)
	slices.Sort(found)
	same := slices.Equal(found, r.found)
	r.found = found
	r.mu.Unlock()
	if same {
		return
	}
	r.logf("info", "found servos %v (Servo Settings' scan)", found)
	r.ensureMultiTurn(found)
	r.sendState()
}

// rolesFromGroups assigns the roles from the servo console's groups (members
// leader first) when that's clear: three servos found and one group of two of
// them, which is the elevation (its mirroring the console's), the third the
// azimuth. Otherwise the roles are as they are.
func (r *rig) rolesFromGroups(groups [][]uint8, mirrored func(uint8) bool) {
	r.mu.Lock()
	found := slices.Clone(r.found)
	r.mu.Unlock()
	if len(found) != 3 || r.simulated() { // (the simulator's roles are its own)
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
		ro := r.roles()
		for _, id := range []uint8{ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth} {
			if !slices.Contains(found, id) {
				r.logf("error", "servo %d (in the rig's roles) was not found: check its power and cable, or make a group of the two elevation servos in Servo Settings (the rig takes the roles from it)", id)
			}
		}
	}
	r.sendState()
	return err
}

// ensureMultiTurn switches the found role servos to multi-turn mode (angle
// limits 0/0, saved on the servo) if they aren't already.
func (r *rig) ensureMultiTurn(found []uint8) {
	ro := r.roles()
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
func (r *rig) ready(axes ...string) (Roles, error) {
	ro := r.roles()
	r.mu.Lock()
	found := slices.Clone(r.found)
	r.mu.Unlock()
	for _, id := range axisIDs(ro, axes...) {
		if !slices.Contains(found, id) {
			return ro, fmt.Errorf("servo %d was not found on the bus", id)
		}
	}
	return ro, nil
}

// axisIDs are the role servos of axes ("elevation": the two, "azimuth"; none:
// all three). An axis needs only its own: the elevation moves with the
// platform's servo not there, say.
func axisIDs(ro Roles, axes ...string) []uint8 {
	if len(axes) == 0 {
		return []uint8{ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth}
	}
	var ids []uint8
	for _, a := range axes {
		switch a {
		case "elevation":
			ids = append(ids, ro.ElevationLeader, ro.ElevationFollower)
		case "azimuth":
			ids = append(ids, ro.Azimuth)
		}
	}
	return ids
}

// present: the role servos found on the bus (a stop, a wait, torque: for
// those there are), an error if none.
func (r *rig) present() (Roles, []uint8, error) {
	ro := r.roles()
	r.mu.Lock()
	found := slices.Clone(r.found)
	r.mu.Unlock()
	var ids []uint8
	for _, id := range axisIDs(ro) {
		if slices.Contains(found, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return ro, nil, errors.New("none of the rig's servos was found on the bus")
	}
	return ro, ids, nil
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
	m := r.cfg.get().Motion
	if azimuth != nil {
		if err := r.platformRoom(wrap180(*azimuth)); err != nil {
			return err
		}
	}
	return r.moveToAt(elevation, azimuth, m.Speed, m.AzimuthSpeed)
}

// platformRoom: the platform servo's goals reach only about ±7.5 turns from
// where its count started, so near that end the short way to azimuth a is
// out of reach, and gosts turns it a whole turn the other way instead (seen:
// nearly a full turn to go from 0° to 1.1°). Then its count is started
// afresh where it stands (no move: resetTurns), and the move goes the short
// way. (Not for a capture's moving shots: their laps see to it, see spiral.)
func (r *rig) platformRoom(a float64) error {
	ro, err := r.ready("azimuth")
	if err != nil {
		return err
	}
	return r.withBus(func(bus *gosts.Bus) error {
		if err := r.knowTurns(bus, false, ro.Azimuth); err != nil {
			return err
		}
		sv := bus.Servo(ro.Azimuth)
		cur, err := sv.AbsolutePosition()
		if err != nil {
			return err
		}
		goal, err := sv.ShortestGoal(stepsFor(a, ro.InvertAzimuth), -gosts.MultiTurnLimit, gosts.MultiTurnLimit)
		if err != nil {
			return err
		}
		if abs(goal-cur) <= gosts.StepsPerRev/2+8 { // the short way: in reach
			return nil
		}
		if moving, err := sv.Moving(); err != nil || moving {
			return err // (not while it's turning: the move goes as it is)
		}
		r.logf("info", "azimuth: the platform is %.1f turns round, at the end of its servo's reach: its count started afresh where it is (no move), so it goes the short way, not a whole turn back", float64(cur)/gosts.StepsPerRev)
		return resetTurns(bus, ro.Azimuth)
	})
}

// moveToAt is moveTo with a speed per axis (step/s); each axis at its own
// acceleration.
func (r *rig) moveToAt(elevation, azimuth *float64, speedE, speedA int) error {
	var axes []string // (the servos it moves: those needed, no others)
	if elevation != nil {
		axes = append(axes, "elevation")
	}
	if azimuth != nil {
		axes = append(axes, "azimuth")
	}
	ro, err := r.ready(axes...)
	if err != nil {
		return err
	}
	c := r.cfg.get()
	return r.withBus(func(bus *gosts.Bus) error {
		acc := uint8(c.Motion.Acc)
		speed := speedE
		var ids []uint8
		if elevation != nil {
			ids = append(ids, ro.ElevationLeader, ro.ElevationFollower)
		}
		if azimuth != nil {
			ids = append(ids, ro.Azimuth)
		}
		if err := r.knowTurns(bus, false, ids...); err != nil {
			return err
		}
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
			if _, err := bus.Servo(ro.Azimuth).MoveToShortest(stepsFor(a, ro.InvertAzimuth), speedA, uint8(c.Motion.AzimuthAcc)); err != nil {
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
	ro, err := r.ready("azimuth")
	if err != nil {
		return 0, err
	}
	var p float64
	err = r.withBus(func(bus *gosts.Bus) error {
		if err := r.knowTurns(bus, true, ro.Azimuth); err != nil {
			return err
		}
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
	ro, err := r.ready("azimuth")
	if err != nil {
		return err
	}
	return r.withBus(func(bus *gosts.Bus) error {
		if err := resetTurns(bus, ro.Azimuth); err != nil {
			return err
		}
		return bus.Servo(ro.Azimuth).EnableTorque(true) // (holding there, as the capture goes on)
	})
}

// resetTurns starts servo id's turn count afresh where it is, without
// moving it (see resetPlatformTurns): its goal then where it is, its torque
// as it was. Checked: if its angle moved or the count isn't within a turn
// after, it says so.
func resetTurns(bus *gosts.Bus, id uint8) error {
	sv := bus.Servo(id)
	on, err := sv.TorqueEnabled()
	if err != nil {
		return err
	}
	if on { // (calibrating while it holds could jump: let go a moment)
		if err := sv.EnableTorque(false); err != nil {
			return err
		}
	}
	if err := sv.RestartTurns(); err != nil {
		return err
	}
	at, err := sv.RestartedPosition() // (where it is in the new count: the reading, physical)
	if err != nil {
		return err
	}
	if err := sv.SetGoal(at); err != nil { // its goal where it is (that switches torque on)
		return err
	}
	if !on {
		if err := sv.EnableTorque(false); err != nil {
			return err
		}
	}
	if got, err := sv.AbsolutePosition(); err != nil || absInt(got-at) > gosts.StepsPerRev/2 {
		return fmt.Errorf("servo %d's count is %d after the reset, want %d (%v)", id, got, at, err)
	}
	return nil
}

// unwindPlatform turns the platform whole turns (logical: + like azimuth),
// back to the same angle: its servo's multi-turn goals reach only about
// ±7.5 turns from where it powered up (a power cycle is the only reset:
// switching it to single-turn and back doesn't), so a long moving-shots
// spiral unwinds it between laps. Briskly (unwindSpeed), at the configured
// acceleration (the platform's); it returns once the platform is there.
func (r *rig) unwindPlatform(ctx context.Context, turns int) error {
	ro, err := r.ready("azimuth")
	if err != nil {
		return err
	}
	acc := uint8(r.cfg.get().Motion.AzimuthAcc)
	raw := turns
	if ro.InvertAzimuth {
		raw = -raw
	}
	var goal int
	if err := r.withBus(func(bus *gosts.Bus) error {
		if err := r.knowTurns(bus, false, ro.Azimuth); err != nil {
			return err
		}
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
	_, ids, err := r.present()
	if err != nil {
		return err
	}
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
	ro, ids, err := r.present()
	if err != nil {
		return err
	}
	return r.withBus(func(bus *gosts.Bus) error {
		var errs []error
		if slices.Contains(ids, ro.ElevationLeader) && slices.Contains(ids, ro.ElevationFollower) {
			errs = append(errs, bus.Group(ro.ElevationLeader, ro.ElevationFollower).Stop())
		} else {
			for _, id := range []uint8{ro.ElevationLeader, ro.ElevationFollower} {
				if slices.Contains(ids, id) {
					errs = append(errs, bus.Servo(id).Stop())
				}
			}
		}
		if slices.Contains(ids, ro.Azimuth) {
			errs = append(errs, bus.Servo(ro.Azimuth).Stop())
		}
		return errors.Join(errs...)
	})
}

// torque switches torque on (holding) or off for an axis' servos:
// "elevation" (the two), "azimuth", or "" all three.
func (r *rig) torque(on bool, axis ...string) error {
	var ids []uint8
	if len(axis) > 0 && axis[0] != "" {
		if axis[0] != "elevation" && axis[0] != "azimuth" {
			return fmt.Errorf("no axis %q", axis[0])
		}
		ro, err := r.ready(axis[0])
		if err != nil {
			return err
		}
		ids = axisIDs(ro, axis[0])
	} else { // (all three: those there are)
		var err error
		if _, ids, err = r.present(); err != nil {
			return err
		}
	}
	defer r.torqueStale.Store(true)
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

// knowTurns makes sure the servos' turn counts are known first. A
// multi-turn servo with torque off that was turned past the 0/4095 seam from
// its goal (by hand, say) has none (gosts.ErrTurnUnknown): moving it then
// could take it a whole turn the wrong way. Those have their count started
// afresh where they are (resetTurns: no move; holding with the stale goal
// would pull them to it, and switching multi-turn off and on doesn't reset
// the count), then Torque Lock (as the page's switch, checked), and with
// release they're let go again after: the count's known all the same then
// (their goal is where they are).
func (r *rig) knowTurns(bus *gosts.Bus, release bool, ids ...uint8) error {
	var unknown []string
	var lock []uint8
	for _, id := range ids {
		_, err := bus.Servo(id).AbsolutePosition()
		switch {
		case errors.Is(err, gosts.ErrTurnUnknown):
			unknown = append(unknown, fmt.Sprintf("#%d", id))
			lock = append(lock, id)
		case err != nil:
			return fmt.Errorf("servo %d: %w", id, err)
		}
	}
	if len(lock) == 0 {
		return nil
	}
	lock = ids // (all of them, held together: the elevation pair on one swing, one not left to hold it alone)
	r.logf("info", "servo %s: turn count unknown (turned by hand with torque off?): learning it (held a moment where it is%s)", strings.Join(unknown, ", "), map[bool]string{true: ", then let go", false: ""}[release])
	defer r.torqueStale.Store(true) // (the page's switches: on)
	// (Torque Lock, checked: Hold restarts each one's count where it is and
	// holds there, in it)
	if err := holdChecked(bus, lock); err != nil {
		return fmt.Errorf("learning the turn count: %w", err)
	}
	if release {
		return bus.SyncTorque(false, lock...)
	}
	return nil
}

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

// stillFor: how long a servo let go is left still before its turn count's
// learned (not while it's being turned by hand).
const stillFor = 1500 * time.Millisecond

// learnRetry: how long before a learn that didn't hold (the servo moved:
// still being turned by hand, say) is tried again.
var learnRetry = 30 * time.Second

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// powerSag watches the servos' supply voltage (telemetry's): a drop well
// under what it's been (seen: 12.2 V to 5.0 V as the elevation started to
// move, a USB-C PD charger resetting to its 5 V under the load), and the
// servos going silent after, restarting (torque off, turn counts lost), is
// said as what it is, not left as timeouts.
type powerSag struct {
	normal float64           // the voltage as it's been (highest lately, easing down)
	low    float64           // the lowest in a sag going on (0: none)
	min    map[uint8]float64 // this tick's
	silent time.Time         // when the servos went silent, in a sag (zero: they didn't)
	back   time.Time         // when they answered again
	said   time.Time
}

func (p *powerSag) seen(id uint8, v float64) {
	if p.min == nil {
		p.min = map[uint8]float64{}
	}
	p.min[id] = v
}

// check, once a tick (asked: how many servos were asked): a line for the
// log when a sag's over (the servos going silent in it: said too).
func (p *powerSag) check(now time.Time, asked int) string {
	defer clear(p.min)
	lo := 0.0
	for _, v := range p.min {
		if v > 0 && (lo == 0 || v < lo) {
			lo = v
		}
	}
	if lo == 0 { // (none answered)
		if p.low > 0 && asked > 0 && p.silent.IsZero() {
			p.silent = now
		}
		return ""
	}
	if !p.silent.IsZero() && p.back.IsZero() {
		p.back = now
	}
	if p.normal == 0 || lo > p.normal {
		p.normal = lo
	}
	if lo < p.normal*sagShare {
		if p.low == 0 || lo < p.low {
			p.low = lo
		}
		return ""
	}
	p.normal = math.Max(lo, p.normal*0.999) // (a supply changed for a lower one: followed, slowly)
	if p.low == 0 {
		return ""
	}
	low, silent := p.low, time.Duration(0)
	if !p.silent.IsZero() {
		silent = p.back.Sub(p.silent)
	}
	p.low, p.silent, p.back = 0, time.Time{}, time.Time{}
	if now.Sub(p.said) < 5*time.Second {
		return ""
	}
	p.said = now
	msg := fmt.Sprintf("the servos' power sagged to %.1f V (from %.1f V)", low, p.normal)
	if silent > 0 {
		msg += fmt.Sprintf(", and they went silent %.1f s (restarted: torque off, turn counts lost)", silent.Seconds())
	}
	if math.Abs(low-5) < 0.4 {
		msg += ": 5 V is a USB-C PD charger's default, dropped back to under the load: a supply that gives the current the servos draw starting (a 12 V one of 5 A or more), or a large capacitor on the board's power in"
	} else {
		msg += ": the supply can't give the current the servos draw starting (a bigger one, thicker or shorter wires, or a large capacitor on the board's power in)"
	}
	return msg
}

// sagShare: a voltage under this share of the usual is a sag.
const sagShare = 0.8

// pollLoop streams telemetry while connected.
func (r *rig) pollLoop(ctx context.Context) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	n := 0
	torque := map[uint8]bool{}
	still := map[uint8]struct {
		pos   int
		since time.Time
	}{}
	retry := map[uint8]time.Time{} // (learning a turn count that didn't work: not again till then)
	var sag powerSag
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ro := r.roles()
		r.mu.Lock()
		connected, scanning := r.port != "", r.scanning
		tg := r.target
		found := slices.Clone(r.found)
		r.mu.Unlock()
		if !connected || scanning {
			continue
		}
		roles := map[uint8]string{ro.ElevationLeader: "elevation", ro.ElevationFollower: "elevation", ro.Azimuth: "azimuth"}
		ids := []uint8{ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth}
		var asked []uint8 // (those found: one that isn't, not asked each time)
		for _, id := range ids {
			if slices.Contains(found, id) {
				asked = append(asked, id)
			}
		}
		msg := telemetry{Type: "telemetry", Time: time.Now().UnixMilli(), Servos: map[string]servoStatus{}}
		lost := false // (the board off USB)
		_ = r.withBus(func(bus *gosts.Bus) error {
			res := map[uint8]gosts.FeedbackResult{}
			if len(asked) > 0 {
				var err error
				if res, err = bus.SyncFeedback(asked...); portGone(err) {
					lost = true
					return err
				}
			}
			if n%10 == 0 || r.torqueStale.Swap(false) { // torque switch once a second (not part of the feedback), or just changed
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
				case !ok && !slices.Contains(found, id):
					st.Error = "not found on the bus"
				case !ok:
					st.Error = "no reply"
				case f.Err != nil:
					st.Error = f.Err.Error()
				default:
					st.Pos, st.Moving, st.Load, st.Volt, st.Temp, st.Status = f.Position, f.Moving, f.Load, f.Voltage, f.Temperature, f.Status.String()
					sag.seen(id, f.Voltage)
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
			// A servo let go (torque off) and left still a moment, its turn
			// count lost (turned past the seam by hand): learned at once
			// (held a moment where it is, then let go again), not left to
			// the next move.
			now := time.Now()
			for _, id := range ids {
				f, ok := res[id]
				if !ok || f.Err != nil {
					delete(still, id)
					continue
				}
				if s, ok := still[id]; !ok || absInt(f.Position-s.pos) > 2 {
					still[id] = struct {
						pos   int
						since time.Time
					}{f.Position, now}
				}
				if n%10 != 5 || torque[id] || f.Moving || now.Sub(still[id].since) < stillFor || now.Before(retry[id]) {
					continue
				}
				if _, err := bus.Servo(id).AbsolutePosition(); !errors.Is(err, gosts.ErrTurnUnknown) {
					continue
				}
				// (one of the elevation pair: both, together, if its partner's
				// let go too: one holding the swing alone would strain)
				learn := []uint8{id}
				if pair := []uint8{ro.ElevationLeader, ro.ElevationFollower}; slices.Contains(pair, id) {
					other := pair[0]
					if other == id {
						other = pair[1]
					}
					if slices.Contains(asked, other) && !torque[other] {
						learn = append(learn, other)
					}
				}
				if err := r.knowTurns(bus, true, learn...); err != nil {
					r.logf("error", "servo %d: %v", id, err)
					for _, l := range learn {
						retry[l] = now.Add(learnRetry)
					}
				}
			}
			return nil
		})
		if lost {
			r.portLost(ctx)
			continue
		}
		if msg := sag.check(time.Now(), len(asked)); msg != "" {
			r.logf("error", "%s", msg)
		}
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
	c := r.cfg.get()
	c.Roles = r.rolesFor(r.port) // (the simulator's own)
	return stateMsg{Type: "state", Connected: r.port != "", Port: r.port, Scanning: r.scanning, Found: found, Config: c}
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
	ro := r.roles()
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
