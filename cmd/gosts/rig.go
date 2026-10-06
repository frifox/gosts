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

// simPort selects the simulated board (servos 10, 11 and 12).
const simPort = "sim"

// rig is the connection to the driver board and the rig's three servos.
type rig struct {
	cfg *configFile
	out func(msg any) // broadcast to every window

	busMu sync.RWMutex
	bus   *gosts.Bus

	mu       sync.Mutex
	port     string
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
		bus, err = gosts.NewBus(servosim.NewPort(10, 11, 12))
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
	r.port, r.found = port, nil
	r.mu.Unlock()
	r.logf("info", "connected to %s", port)
	return r.scan(ctx)
}

func (r *rig) disconnect() {
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
	r.sendState()
	return nil
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
	r.mu.Unlock()
	if err != nil {
		r.logf("error", "scan: %v", err)
	} else {
		r.logf("info", "found servos %v", found)
		if r.cfg.get().Motion.MultiTurn {
			r.ensureMultiTurn(found)
		}
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
	ro, err := r.ready()
	if err != nil {
		return err
	}
	c := r.cfg.get()
	return r.withBus(func(bus *gosts.Bus) error {
		speed, acc := c.Motion.Speed, uint8(c.Motion.Acc)
		if elevation != nil {
			e := math.Max(c.Motion.ElevationMin, math.Min(c.Motion.ElevationMax, *elevation))
			g := bus.Group(ro.ElevationLeader, ro.ElevationFollower)
			if _, err := g.MoveToShortest(stepsFor(e, ro.InvertElevation), speed, acc); err != nil {
				return fmt.Errorf("elevation: %w", err)
			}
			r.mu.Lock()
			r.target.elevation, r.target.set = e, true
			r.mu.Unlock()
		}
		if azimuth != nil {
			a := wrap180(*azimuth)
			if _, err := bus.Servo(ro.Azimuth).MoveToShortest(stepsFor(a, ro.InvertAzimuth), speed, acc); err != nil {
				return fmt.Errorf("azimuth: %w", err)
			}
			r.mu.Lock()
			r.target.azimuth, r.target.set = a, true
			r.mu.Unlock()
		}
		return nil
	})
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
	return r.withBus(func(bus *gosts.Bus) error {
		return bus.SyncTorque(on, ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth)
	})
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
