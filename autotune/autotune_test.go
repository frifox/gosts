package autotune

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/frifox/gosts"
)

// plant is a crude servo + load: inertia, friction, optional gravity, and a
// controller shaped by P, D, I, start force and dead zone that follows a
// speed/acceleration-limited profile to the goal like the servo does. It runs
// on virtual time (5 ms per read).
type plant struct {
	p           Params
	pos, vel    float64
	goal        float64
	sp, spVel   float64 // profiled setpoint
	speed, acc  float64 // of the current move (step/s, step/s²)
	integ       float64
	gravity     float64 // constant pull towards lower positions (step/s²), like an unbalanced arm
	u           float64 // last drive, reported as load
	now         time.Time
	members     int
	offset      float64 // second member's position offset (group test)
	failAt      int     // report overload after this many reads (0 = never)
	reads       int
	applied     []Params
	inertiaMul  float64
	glitchEvery int // report one bogus temperature every n reads
	hotAfter    int // report a sustained high temperature after n reads
}

func newPlant(p Params) *plant {
	return &plant{p: p, pos: 2048, goal: 2048, sp: 2048, now: time.Unix(0, 0), members: 1, inertiaMul: 1, speed: 1000, acc: 3000}
}

func (f *plant) step(dt float64) {
	// Setpoint profile: accelerate, cruise, brake to stop on the goal.
	d := f.goal - f.sp
	vmax := math.Min(f.speed, math.Sqrt(2*f.acc*math.Abs(d)))
	want := math.Copysign(vmax, d)
	dv := math.Max(-f.acc*dt, math.Min(f.acc*dt, want-f.spVel))
	f.spVel += dv
	f.sp += f.spVel * dt
	if math.Abs(f.goal-f.sp) < 0.5 && math.Abs(f.spVel) < f.acc*dt*2 {
		f.sp, f.spVel = f.goal, 0
	}

	err := f.sp - f.pos
	f.integ = math.Max(-400, math.Min(400, f.integ+err*dt))
	var u float64
	if math.Abs(err) > float64(f.p.DeadZone) {
		u = float64(f.p.P)*err*0.6 - float64(f.p.D)*f.vel*0.3 + float64(f.p.I)*f.integ*2
		if s := float64(f.p.MinStart) * 2; math.Abs(u) < s {
			u = math.Copysign(s, u)
		}
	} else {
		u = -float64(f.p.D)*f.vel*0.3 + float64(f.p.I)*f.integ*2
	}
	f.u = u
	u -= f.gravity
	u = math.Max(-3000, math.Min(3000, u))
	const friction = 60
	acc := u / f.inertiaMul
	if math.Abs(f.vel) < 1 && math.Abs(u) < friction {
		f.vel, acc = 0, 0
	} else {
		acc -= math.Copysign(friction, f.vel) / f.inertiaMul
	}
	f.vel += acc * dt
	f.vel = math.Max(-3400, math.Min(3400, f.vel))
	f.pos += f.vel * dt
}

func (f *plant) Params() (Params, error) { return f.p, nil }
func (f *plant) Apply(p Params) error {
	p.Acc = 0 // a move setting, not stored on the servo
	f.p = p
	f.applied = append(f.applied, p)
	return nil
}
func (f *plant) Save(p Params) error     { return f.Apply(p) }
func (f *plant) Position() (int, error)  { return int(math.Round(f.pos)), nil }
func (f *plant) Limits() (int, int, error) {
	return 0, 4095, nil
}
func (f *plant) MoveTo(pos, speed int, acc uint8) error {
	f.goal, f.speed, f.acc = float64(pos), float64(speed), float64(acc)*100
	if speed == 0 {
		f.speed = 3400
	}
	if acc == 0 {
		f.acc = 25000
	}
	return nil
}
func (f *plant) Stop() error { f.goal, f.sp, f.spVel = f.pos, f.pos, 0; return nil }
func (f *plant) Now() time.Time                   { return f.now }
func (f *plant) Read() ([]Reading, error) {
	for i := 0; i < 5; i++ {
		f.step(0.001)
	}
	f.now = f.now.Add(5 * time.Millisecond)
	f.reads++
	fb := gosts.Feedback{Position: int(math.Round(f.pos)), Moving: math.Abs(f.vel) > 5, Load: f.u / 30, Current: math.Abs(f.vel) / 5}
	if f.failAt > 0 && f.reads > f.failAt {
		fb.Status = gosts.StatusOverload
	}
	fb.Temperature = 32
	if f.glitchEvery > 0 && f.reads%f.glitchEvery == 0 {
		fb.Temperature = 91 // a single bad sample
	}
	if f.hotAfter > 0 && f.reads > f.hotAfter {
		fb.Temperature = 80 // really overheating
	}
	out := []Reading{{ID: 1, Feedback: fb}}
	if f.members > 1 {
		fb2 := fb
		fb2.Position = int(math.Round(f.pos + f.offset))
		out = append(out, Reading{ID: 2, Feedback: fb2})
	}
	return out, nil
}

func TestTunesAnUnderdampedServo(t *testing.T) {
	start := Params{P: 48, D: 16, MinStart: 16, DeadZone: 1}
	f := newPlant(start)
	var progress []Progress
	res, err := Run(context.Background(), f, Options{Progress: func(p Progress) { progress = append(progress, p) }})
	if err != nil {
		t.Fatal(err)
	}
	b, a := res.Before.Metrics, res.Best.Metrics
	t.Logf("before %v: %+v", res.Before.Params, b)
	t.Logf("best   %v: %+v", res.Best.Params, a)
	if a.Score >= b.Score || a.Overshoot > b.Overshoot || a.Wobble > b.Wobble {
		t.Fatal("tuning did not improve the response")
	}
	if !a.Settled {
		t.Fatal("best values don't settle")
	}
	want := res.Best.Params
	want.Acc = 0
	if f.p != want {
		t.Fatal("best values not left applied")
	}
	if p, _ := f.Position(); abs(p-2048) > 3 {
		t.Fatal("not back at the start position", p)
	}
	if len(progress) != len(res.Trials) || progress[0].Phase != "baseline" {
		t.Fatal("progress", len(progress), len(res.Trials))
	}
}

func TestKeepsGoodValues(t *testing.T) {
	// Already well damped: the result must never be worse than the start.
	f := newPlant(Params{P: 32, D: 64, MinStart: 16, DeadZone: 1})
	res, err := Run(context.Background(), f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Best.Metrics.Score > res.Before.Metrics.Score {
		t.Fatal("result worse than the starting values")
	}
}

func TestAbortRestoresValues(t *testing.T) {
	start := Params{P: 32, D: 32, MinStart: 16, DeadZone: 1}
	f := newPlant(start)
	f.failAt = 2000
	_, err := Run(context.Background(), f, Options{})
	if !errors.Is(err, ErrAborted) {
		t.Fatal("want ErrAborted, got", err)
	}
	if f.p != start {
		t.Fatal("original values not restored", f.p)
	}
}

func TestCancelRestoresValues(t *testing.T) {
	start := Params{P: 32, D: 32, MinStart: 16, DeadZone: 1}
	f := newPlant(start)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := Run(ctx, f, Options{Progress: func(p Progress) {
		if p.Trial == 3 {
			cancel()
		}
	}})
	if !errors.Is(err, context.Canceled) || f.p != start {
		t.Fatal(err, f.p)
	}
}

func TestGroupSpreadPenalty(t *testing.T) {
	f := newPlant(Params{P: 32, D: 48, MinStart: 16, DeadZone: 1})
	f.members, f.offset = 2, 6 // the second member sits 6 steps off
	res, err := Run(context.Background(), f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Before.Metrics.Spread != 6 {
		t.Fatal("spread not measured", res.Before.Metrics.Spread)
	}
}

func TestRefusesWithoutRoom(t *testing.T) {
	f := newPlant(Params{P: 32, D: 32})
	f.pos, f.goal = 10, 10 // at the lower angle limit
	if _, err := Run(context.Background(), f, Options{}); err == nil {
		t.Fatal("expected an error near the limit")
	}
}

func TestTemperatureGlitchIgnored(t *testing.T) {
	f := newPlant(Params{P: 32, D: 48, MinStart: 16, DeadZone: 1})
	f.glitchEvery = 500
	if _, err := Run(context.Background(), f, Options{}); err != nil {
		t.Fatal("a single bad temperature sample aborted the run:", err)
	}
}

func TestSustainedOverheatAborts(t *testing.T) {
	start := Params{P: 32, D: 48, MinStart: 16, DeadZone: 1}
	f := newPlant(start)
	f.hotAfter = 1500
	_, err := Run(context.Background(), f, Options{})
	if !errors.Is(err, ErrAborted) || f.p != start {
		t.Fatal("want abort with values restored, got", err, f.p)
	}
}

func TestGravitySag(t *testing.T) {
	// An unbalanced arm held horizontally: gravity pulls it below every goal.
	f := newPlant(Params{P: 32, D: 48, MinStart: 16, DeadZone: 1})
	f.gravity = 500
	res, err := Run(context.Background(), f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, a := res.Before.Metrics, res.Best.Metrics
	t.Logf("before %v: %+v", res.Before.Params, b)
	t.Logf("best   %v: %+v", res.Best.Params, a)
	if b.FinalError < 10 {
		t.Fatal("the plant should sag with the starting values", b.FinalError)
	}
	if res.Best.Params.I == 0 || a.FinalError > 2 {
		t.Fatal("sag not removed")
	}
	if a.Wobble > b.Wobble+1 || a.Overshoot > b.Overshoot+5 {
		t.Fatal("removing the sag made it wobble")
	}
	if a.HoldLoad < 10 {
		t.Fatal("holding load not measured", a.HoldLoad)
	}
}
