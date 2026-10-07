package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/frifox/gosts"
	servosim "github.com/frifox/gosts/cmd/gosts-ctl/servo-sim"
)

func TestPlanShots(t *testing.T) {
	m := Motion{ElevationMin: -30, ElevationMax: 90}
	for _, n := range []int{1, 7, 60, 250} {
		shots, rows, spacing, err := planShots(Plan{Photos: n}, m)
		if err != nil || len(shots) != n {
			t.Fatal(n, len(shots), err)
		}
		// All inside the range.
		for _, s := range shots {
			if s.Elevation < -30 || s.Elevation > 90 {
				t.Fatalf("n=%d: %+v outside the range", n, s)
			}
		}
		// Even: every point's nearest neighbour is about one spacing away.
		if n >= 7 {
			pos := func(s shot) [3]float64 {
				e, a := rad(s.Elevation), rad(s.Azimuth)
				return [3]float64{math.Cos(e) * math.Cos(a), math.Cos(e) * math.Sin(a), math.Sin(e)}
			}
			lo, hi := math.Inf(1), 0.0
			for i, a := range shots {
				best := math.Inf(1)
				for j, b := range shots {
					if i != j {
						pa, pb := pos(a), pos(b)
						d := deg(math.Acos(math.Min(1, pa[0]*pb[0]+pa[1]*pb[1]+pa[2]*pb[2])))
						best = math.Min(best, d)
					}
				}
				lo, hi = math.Min(lo, best), math.Max(hi, best)
			}
			t.Logf("n=%d rows=%d spacing %.1f°: nearest neighbours %.1f°…%.1f°", n, rows, spacing, lo, hi)
			if lo < 0.5*spacing || hi > 1.6*spacing {
				t.Fatalf("n=%d: uneven: nearest neighbours %.1f°…%.1f° for a spacing of %.1f°", n, lo, hi, spacing)
			}
		}
		// Order: rows go up in elevation, alternating azimuth direction.
		for i := 1; i < n; i++ {
			a, b := shots[i-1], shots[i]
			if a.Ring == b.Ring {
				if dir := b.Azimuth - a.Azimuth; (a.Ring%2 == 0) != (dir >= 0) {
					t.Fatalf("n=%d: row %d not in azimuth order at %d", n, a.Ring, i)
				}
			} else if b.Ring != a.Ring+1 {
				t.Fatalf("n=%d: rows out of order", n)
			}
		}
	}
	if _, _, _, err := planShots(Plan{Photos: 0}, m); err == nil {
		t.Fatal("0 photos accepted")
	}
	if _, _, _, err := planShots(Plan{Photos: 10}, Motion{ElevationMin: 40, ElevationMax: 10}); err == nil {
		t.Fatal("empty range accepted")
	}
}

func TestAngles(t *testing.T) {
	ro := Roles{}
	if e := elevationOf(stepsFor(-20, false), ro); e < -20.1 || e > -19.9 {
		t.Fatal(e)
	}
	ro.InvertElevation = true
	if e := elevationOf(stepsFor(30, true), ro); e < 29.9 || e > 30.1 {
		t.Fatal("inverted", e)
	}
	if a := wrap180(270); a != -90 {
		t.Fatal(a)
	}
}

func TestSimStartPosition(t *testing.T) {
	ro := defaultConfig().Roles // leader mirrored
	bus, err := gosts.NewBus(newSim(ro))
	if err != nil {
		t.Fatal(err)
	}
	bus.SetMirrored(ro.ElevationLeader, ro.LeaderMirrored)
	for _, id := range []uint8{ro.ElevationLeader, ro.ElevationFollower} {
		f, err := bus.Servo(id).Feedback()
		if err != nil {
			t.Fatal(err)
		}
		if e := elevationOf(f.Position, ro); math.Abs(e-simElevation) > 0.2 {
			t.Fatalf("servo %d starts at elevation %.1f", id, e)
		}
	}
	f, _ := bus.Servo(ro.Azimuth).Feedback()
	if a := azimuthOf(f.Position, ro); math.Abs(a-simAzimuth) > 0.2 {
		t.Fatalf("platform starts at azimuth %.1f", a)
	}
}

func TestSpiralOrder(t *testing.T) {
	m := Motion{ElevationMin: -45, ElevationMax: 80}
	shots, turns, spacing, err := planShots(Plan{Photos: 120, Moving: true}, m)
	if err != nil || len(shots) != 120 {
		t.Fatal(len(shots), err)
	}
	// The turntable keeps turning one way: every step forward in azimuth.
	backwards, maxStep, total := 0, 0.0, 0.0
	for i := 1; i < len(shots); i++ {
		da := wrap180(shots[i].Azimuth - shots[i-1].Azimuth)
		if da < 0 {
			backwards++
		}
		total += da
		a, b := shots[i-1], shots[i]
		pa := [3]float64{math.Cos(rad(a.Elevation)) * math.Cos(rad(a.Azimuth)), math.Cos(rad(a.Elevation)) * math.Sin(rad(a.Azimuth)), math.Sin(rad(a.Elevation))}
		pb := [3]float64{math.Cos(rad(b.Elevation)) * math.Cos(rad(b.Azimuth)), math.Cos(rad(b.Elevation)) * math.Sin(rad(b.Azimuth)), math.Sin(rad(b.Elevation))}
		maxStep = math.Max(maxStep, deg(math.Acos(math.Min(1, pa[0]*pb[0]+pa[1]*pb[1]+pa[2]*pb[2]))))
	}
	t.Logf("%d turns, spacing %.1f°: %d backward steps, %.0f° of turntable travel, largest hop %.1f°", turns, spacing, backwards, total, maxStep)
	if backwards > len(shots)/20 {
		t.Fatalf("%d steps go backwards", backwards)
	}
	if total < float64(turns-1)*360 || total > float64(turns+1)*360 {
		t.Fatalf("turntable travel %.0f° for %d turns", total, turns)
	}
	if maxStep > 3*spacing {
		t.Fatalf("a hop of %.1f° (spacing %.1f°)", maxStep, spacing)
	}
	// It climbs: the first shots low, the last high.
	if shots[0].Elevation > 0 || shots[len(shots)-1].Elevation < 40 {
		t.Fatal("not a rising spiral", shots[0].Elevation, shots[len(shots)-1].Elevation)
	}
}

func TestTrajectory(t *testing.T) {
	e := []float64{0, 10, 20, 30}
	a := []float64{170, -170, -150, -130} // crosses ±180 the short way
	// With a camera needing 2 s between photos, every hop takes at least that.
	if slow := newTrajectory(e, a, 50, 100, 2); true {
		for i := 1; i < len(slow.pointT); i++ {
			if d := slow.pointT[i] - slow.pointT[i-1]; d < 2-1e-6 {
				t.Errorf("hop %d takes %.2f s, under the camera's 2 s", i, d)
			}
		}
	}
	tr := newTrajectory(e, a, 50, 100, 0)
	if d := tr.duration(); d <= 0 {
		t.Fatalf("duration %v", d)
	}
	// It passes each point, at rest at the ends, never over the axis speed.
	for i := range e {
		ge, ga := tr.at(tr.pointT[i])
		if math.Abs(ge-e[i]) > 1e-6 || math.Abs(wrap180(ga-a[i])) > 1e-6 {
			t.Errorf("point %d: at %v,%v, want %v,%v", i, ge, ga, e[i], a[i])
		}
	}
	for k := 1; k < len(tr.t); k++ {
		dt := tr.t[k] - tr.t[k-1]
		if dt <= 0 {
			continue
		}
		if r := math.Max(math.Abs(tr.e[k]-tr.e[k-1]), math.Abs(tr.a[k]-tr.a[k-1])) / dt; r > 50*1.001 {
			t.Fatalf("sample %d: %v°/s, over 50", k, r)
		}
	}
	if math.Abs(tr.a[len(tr.a)-1]-(170+60)) > 1e-6 {
		t.Errorf("azimuth not unwrapped: ends at %v", tr.a[len(tr.a)-1])
	}
}

func TestCaptureCamera(t *testing.T) {
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{cfg: cfg, out: func(any) {}}
	if err := r.connect(context.Background(), simPort, 0); err != nil {
		t.Fatal(err)
	}
	defer r.disconnect()
	camera := &cameraConn{out: func(any) {}}
	c := &capture{rig: r, camera: camera, out: func(any) {}}
	p := Plan{Photos: 3, SettleMS: 0}
	if err := c.start(p); err == nil {
		t.Fatal("started without a camera")
	}
	if err := camera.connect(simCameraID); err != nil {
		t.Fatal(err)
	}
	if err := c.start(p); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		m := c.msg()
		if !m.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("capture didn't finish")
		}
		time.Sleep(50 * time.Millisecond)
	}
	camera.mu.Lock()
	n := camera.count
	camera.mu.Unlock()
	if m := c.msg(); m.Index != 3 || n != 3 {
		t.Fatalf("%d of 3 shots done, %d photos taken: %s", m.Index, n, m.Note)
	}
}

func TestSimCameraPicture(t *testing.T) {
	simPictureWait = 100 * time.Millisecond
	defer func() { simPictureWait = 2 * time.Second }()
	c := &cameraConn{out: func(any) {}}
	if err := c.connect(simCameraID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// A page renders photo 1: that's the picture, the made-up one doesn't
	// replace it.
	if err := c.shoot(ctx, true, nil); err != nil {
		t.Fatal(err)
	}
	if !c.simPicture(1, []byte("rendered")) {
		t.Fatal("page's picture refused")
	}
	time.Sleep(200 * time.Millisecond)
	if p, n := c.lastPhoto(); n != 1 || string(p.JPEG) != "rendered" {
		t.Fatalf("photo %d: %q", n, p.JPEG)
	}
	// No page renders photo 2: a made-up picture stands in.
	if err := c.shoot(ctx, true, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if p, n := c.lastPhoto(); n != 2 || len(p.JPEG) < 1000 || p.JPEG[0] != 0xFF {
		t.Fatalf("photo %d: %d bytes", n, len(p.JPEG))
	}
	if c.simPicture(2, []byte("late")) {
		t.Fatal("a late copy replaced the picture")
	}
}

func TestPhotoTimeline(t *testing.T) {
	simPictureWait = 50 * time.Millisecond
	defer func() { simPictureWait = 2 * time.Second }()
	c := &cameraConn{out: func(any) {}}
	if err := c.connect(simCameraID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := c.shoot(context.Background(), true, nil); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(150 * time.Millisecond)
	tl := c.timeline().Photos
	if len(tl) != 3 || tl[0].N != 1 || tl[2].N != 3 {
		t.Fatalf("timeline %v", tl)
	}
	if _, ok := c.photo(2); !ok {
		t.Fatal("photo 2 missing")
	}
	c.reset()
	if tl := c.timeline().Photos; len(tl) != 0 {
		t.Fatalf("after Reset: %v", tl)
	}
	// A new batch after Reset: numbering from 1 again.
	c.shoot(context.Background(), true, nil)
	time.Sleep(150 * time.Millisecond)
	if tl := c.timeline().Photos; len(tl) != 1 || tl[0].N != 1 {
		t.Fatalf("after Reset and a photo: %v", tl)
	}
}

// The two elevation servos straddle the 0/4095 seam at 0° (a step or two of
// calibration difference): their own turn counts are then a turn apart, and
// a goal worked out on the leader would send the follower the long way round,
// past the elevation limits. Each must go straight to the target.
func TestElevationAcrossSeam(t *testing.T) {
	defer func(f func(Roles) *servosim.Port) { newSimPort = f }(newSimPort)
	newSimPort = func(ro Roles) *servosim.Port {
		p := servosim.NewPort(ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth)
		// Logical +2 steps on the leader (mirrored: physical 4094), -2 (4094)
		// on the follower.
		p.SetPosition(ro.ElevationLeader, 2*gosts.CenterPosition-2)
		p.SetPosition(ro.ElevationFollower, gosts.StepsPerRev-2)
		p.SetPosition(ro.Azimuth, 0)
		return p
	}
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{cfg: cfg, out: func(any) {}}
	if err := r.connect(context.Background(), simPort, 0); err != nil {
		t.Fatal(err)
	}
	defer r.disconnect()
	ro := cfg.get().Roles
	if err := r.torque(true); err != nil {
		t.Fatal(err)
	}
	read := func() (float64, float64) {
		var le, fe float64
		r.withBus(func(bus *gosts.Bus) error {
			res, _ := bus.SyncFeedback(ro.ElevationLeader, ro.ElevationFollower)
			le, fe = elevationOf(res[ro.ElevationLeader].Position, ro), elevationOf(res[ro.ElevationFollower].Position, ro)
			return nil
		})
		return le, fe
	}
	target := -25.0
	if err := r.moveTo(&target, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		le, fe := read()
		for _, e := range []float64{le, fe} {
			if e > 1 || e < target-2 {
				t.Fatalf("on the way from 0° to %v°, an elevation servo is at %.1f° (leader %.1f°, follower %.1f°)", target, e, le, fe)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if le, fe := read(); math.Abs(le-target) > 1 || math.Abs(fe-target) > 1 {
		t.Fatalf("ended at leader %.1f°, follower %.1f°, want %v°", le, fe, target)
	}
}

func TestPhotoDelete(t *testing.T) {
	simPictureWait = 50 * time.Millisecond
	defer func() { simPictureWait = 2 * time.Second }()
	var events []string
	var mu sync.Mutex
	c := &cameraConn{out: func(m any) {
		mu.Lock()
		defer mu.Unlock()
		switch m := m.(type) {
		case photoEventMsg:
			events = append(events, fmt.Sprint(m.Type, m.N))
		case photoMsg:
			events = append(events, fmt.Sprint("photo", m.N))
		}
	}}
	c.connect(simCameraID)
	c.shoot(context.Background(), true, nil) // 1: deleted once it's there
	c.shoot(context.Background(), true, nil) // 2: deleted while its picture is still coming
	c.delete(2)
	time.Sleep(150 * time.Millisecond)
	c.delete(1)
	if tl := c.timeline().Photos; len(tl) != 0 {
		t.Fatalf("timeline %v", tl)
	}
	mu.Lock()
	defer mu.Unlock()
	want := "photoPending1 photoPending2 photoDeleted2 photo1 photoDeleted1"
	if got := strings.Join(events, " "); got != want {
		t.Fatalf("events %q, want %q", got, want)
	}
}

// A servo whose own turn count is a turn off from its reading (the
// simulator doesn't restart the count, as the real servo does when Hold
// toggles multi-turn): told to hold its reading, it heads a whole turn away.
// Torque on must catch it at the limited torque and switch off again.
func TestTorqueOnCatchesTurnOff(t *testing.T) {
	defer func(f func(Roles) *servosim.Port) { newSimPort = f }(newSimPort)
	newSimPort = func(ro Roles) *servosim.Port {
		p := servosim.NewPort(ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth)
		p.SetPosition(ro.ElevationLeader, 2*gosts.CenterPosition+20) // logical -20
		p.SetPosition(ro.ElevationFollower, -20)                     // reads 4076, counts -20
		p.SetPosition(ro.Azimuth, 0)
		return p
	}
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{cfg: cfg, out: func(any) {}}
	if err := r.connect(context.Background(), simPort, 0); err != nil {
		t.Fatal(err)
	}
	defer r.disconnect()
	ro := cfg.get().Roles
	err = r.torque(true)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("#%d", ro.ElevationFollower)) {
		t.Fatalf("torque on: %v, want it refused for #%d", err, ro.ElevationFollower)
	}
	r.withBus(func(bus *gosts.Bus) error {
		for _, id := range []uint8{ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth} {
			if on, _ := bus.Servo(id).TorqueEnabled(); on {
				t.Errorf("servo %d still has torque", id)
			}
		}
		return nil
	})
	t.Log(err)
}

// TestPlatformTurnsReset: the platform servo wound 2 turns and more has its
// turn count reset where it is: the same angle, the same zero, under a turn
// now, and the next move goes the short way.
func TestPlatformTurnsReset(t *testing.T) {
	defer func(f func(Roles) *servosim.Port) { newSimPort = f }(newSimPort)
	newSimPort = func(ro Roles) *servosim.Port {
		p := servosim.NewPort(ro.ElevationLeader, ro.ElevationFollower, ro.Azimuth)
		p.SetPosition(ro.ElevationLeader, gosts.CenterPosition)
		p.SetPosition(ro.ElevationFollower, 0)
		p.SetPosition(ro.Azimuth, 0)
		return p
	}
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{cfg: cfg, out: func(any) {}}
	if err := r.connect(context.Background(), simPort, 0); err != nil {
		t.Fatal(err)
	}
	defer r.disconnect()
	if err := r.torque(true); err != nil {
		t.Fatal(err)
	}
	ro := cfg.get().Roles
	zero := func() int {
		var z int
		r.withBus(func(bus *gosts.Bus) error { z, _ = bus.Servo(ro.Azimuth).Zero(); return nil })
		return z
	}
	z0 := zero()
	wound := 2*gosts.StepsPerRev + 1000
	r.withBus(func(bus *gosts.Bus) error { return bus.Servo(ro.Azimuth).MoveTo(wound, 3400, 0) })
	time.Sleep(4 * time.Second)
	before, err := r.platformTurns()
	if err != nil || before < 2 {
		t.Fatalf("wound %.2f turns (%v), want 2 and more", before, err)
	}
	_, a0, _ := r.angles()
	if err := r.resetPlatformTurns(); err != nil {
		t.Fatal(err)
	}
	after, _ := r.platformTurns()
	_, a1, _ := r.angles()
	if after < 0 || after >= 1 || math.Abs(wrap180(a1-a0)) > 0.5 || zero() != z0 {
		t.Fatalf("after the reset: %.2f turns, azimuth %.1f° (was %.1f°), zero %d (was %d)", after, a1, a0, zero(), z0)
	}
	// The next move: the short way, not back the 6 turns.
	target := wrap180(a1 + 90)
	if err := r.moveTo(nil, &target); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	if turns, _ := r.platformTurns(); turns < 0 || turns > 1.5 {
		t.Fatalf("after a 90° move the platform is %.2f turns round", turns)
	}
}

// TestSpiralFit: a spiral from 6.5 turns round fits till it would pass the
// servo's range; unwinding from there takes it back near the other end.
func TestSpiralFit(t *testing.T) {
	var shots []shot
	for i := 0; i < 40; i++ { // 40 shots 45° apart: 5 turns, positive
		shots = append(shots, shot{Azimuth: wrap180(float64(i) * 45)})
	}
	if n := spiralFit(0, shots); n != len(shots) {
		t.Errorf("from 0 turns: %d fit, want all %d", n, len(shots))
	}
	if n := spiralFit(6.5, shots); n == 0 || n >= len(shots) {
		t.Errorf("from 6.5 turns: %d fit, want some, not all", n)
	}
	if n := unwindTurns(7.4, 1); n < 14 || n > 15 {
		t.Errorf("unwinding from 7.4 turns, spiral +: %d turns, want 14", n)
	}
}

// TestTurntableDiameterDefault: a config file from before the turntable
// diameter has the default one.
func TestTurntableDiameterDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	old := "[Rig]\n  BaseX = 600.0\n  BaseY = 500.0\n  PostZ = 400.0\n  SwingX = 600.0\n  SwingY = 450.0\n  CameraOffset = -50.0\n  TurntableZ = 400.0\n  ObjectZ = 100.0\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if r := cfg.get().Rig; r.TurntableD != 240 || r.check() != nil {
		t.Fatalf("turntable diameter %v (%v), want 240", r.TurntableD, r.check())
	}
}
