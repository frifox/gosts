package main

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"
)

// trajectory is a smooth timed path for moving shots: the Catmull-Rom
// curve through the points (elevation and unwrapped azimuth, as the preview
// draws it), timed so that neither axis goes faster than maxRate and the
// speed changes at most by accel, starting and ending at rest, and so that
// the path takes at least minGap seconds from each point to the next (the
// camera's time between photos; 0: no minimum).
type trajectory struct {
	t, e, a []float64 // samples: time (s), elevation, azimuth (degrees)
	pointT  []float64 // when the path passes each point
}

const trajSteps = 32 // samples per hop

func newTrajectory(e, a []float64, maxRate, accel, minGap float64) *trajectory {
	n := len(e)
	az := make([]float64, n) // unwrapped: each hop the short way
	for i := range az {
		if i == 0 {
			az[i] = a[0]
		} else {
			az[i] = az[i-1] + wrap180(a[i]-a[i-1])
		}
	}
	cr := func(p0, p1, p2, p3, t float64) float64 {
		return 0.5 * (2*p1 + (-p0+p2)*t + (2*p0-5*p1+4*p2-p3)*t*t + (-p0+3*p1-3*p2+p3)*t*t*t)
	}
	tr := &trajectory{pointT: make([]float64, n)}
	for i := 0; i+1 < n; i++ {
		i0, i3 := max(0, i-1), min(n-1, i+2)
		for k := 0; k < trajSteps; k++ {
			f := float64(k) / trajSteps
			tr.e = append(tr.e, cr(e[i0], e[i], e[i+1], e[i3], f))
			tr.a = append(tr.a, cr(az[i0], az[i], az[i+1], az[i3], f))
		}
	}
	tr.e = append(tr.e, e[n-1])
	tr.a = append(tr.a, az[n-1])

	// Speed profile, in degrees per second of the faster axis: at most
	// maxRate, changing by at most accel (forward and backward passes), at
	// rest at both ends.
	m := len(tr.e)
	ds := make([]float64, m) // distance from the previous sample
	for i := 1; i < m; i++ {
		ds[i] = math.Max(math.Abs(tr.e[i]-tr.e[i-1]), math.Abs(tr.a[i]-tr.a[i-1]))
	}
	// Each hop's top speed: maxRate, or slower so the hop takes minGap.
	vcap := make([]float64, m)
	for h := 0; h+1 < n; h++ {
		dist := 0.0
		for k := 1; k <= trajSteps; k++ {
			dist += ds[h*trajSteps+k]
		}
		c := maxRate
		if minGap > 0 && dist > 1e-9 {
			c = math.Min(c, dist/minGap)
		}
		for k := 1; k <= trajSteps; k++ {
			vcap[h*trajSteps+k] = c
		}
	}
	v := make([]float64, m)
	for i := 1; i < m; i++ {
		v[i] = math.Min(vcap[i], math.Sqrt(v[i-1]*v[i-1]+2*accel*ds[i]))
	}
	v[m-1] = 0
	for i := m - 2; i >= 0; i-- {
		v[i] = math.Min(v[i], math.Sqrt(v[i+1]*v[i+1]+2*accel*ds[i+1]))
	}
	tr.t = make([]float64, m)
	for i := 1; i < m; i++ {
		dt := 0.0
		if ds[i] > 0 {
			dt = 2 * ds[i] / math.Max(v[i-1]+v[i], 1e-6)
		}
		tr.t[i] = tr.t[i-1] + dt
	}
	for i := range tr.pointT {
		tr.pointT[i] = tr.t[i*trajSteps]
	}
	return tr
}

// duration is how long the path takes.
func (tr *trajectory) duration() float64 { return tr.t[len(tr.t)-1] }

// at is where the path is t seconds in.
func (tr *trajectory) at(t float64) (e, a float64) {
	m := len(tr.t)
	if t <= 0 {
		return tr.e[0], tr.a[0]
	}
	if t >= tr.t[m-1] {
		return tr.e[m-1], tr.a[m-1]
	}
	i := sort.SearchFloat64s(tr.t, t) // tr.t[i-1] < t <= tr.t[i]
	f := (t - tr.t[i-1]) / math.Max(tr.t[i]-tr.t[i-1], 1e-9)
	return tr.e[i-1] + (tr.e[i]-tr.e[i-1])*f, tr.a[i-1] + (tr.a[i]-tr.a[i-1])*f
}

// errPaused: follow stopped because the capture was paused.
var errPaused = errors.New("paused")

const (
	followTick      = 30 * time.Millisecond
	followLookahead = 0.12 // s
)

// follow drives the rig along tr: every tick each axis gets the goal the
// path reaches followLookahead from now, at the speed that gets it there
// from where it really is just then, so the servos keep moving instead of
// stopping at each goal, and the rig runs along the path itself, on time.
// at(k) is called as the path passes point k (the photo; an error stops
// the rig and follow). When paused() says so the rig stops and follow
// returns errPaused.
func (r *rig) follow(ctx context.Context, tr *trajectory, at func(k int) error, paused func() bool, pace func() float64) error {
	// The path's own clock: it runs at pace() (1: as planned; slower, or
	// held at 0, while the camera catches up with the photos asked of it),
	// and stands still while a photo is being asked for.
	vt, last, rate := 0.0, time.Now(), 1.0
	clock := func() float64 {
		now := time.Now()
		rate = 1
		if pace != nil {
			rate = math.Max(0, math.Min(1, pace()))
		}
		vt += now.Sub(last).Seconds() * rate
		last = now
		return vt
	}
	next := 0 // next point
	end := tr.duration()
	for {
		t := clock()
		for next < len(tr.pointT) && t >= tr.pointT[next] {
			if err := at(next); err != nil {
				return errors.Join(err, r.stop())
			}
			next++
			// A photo that held things up doesn't count as path time:
			// the path waits for it instead of racing ahead (and the next
			// photos being due at once, all taken from here).
			last = time.Now()
		}
		if next >= len(tr.pointT) {
			return nil
		}
		if paused() {
			return errors.Join(errPaused, r.stop())
		}
		// Speeds from where the rig really is, so it catches up if it lags.
		e0, a0, err := r.angles()
		if err != nil {
			return err
		}
		// The goal: just ahead on the path (as far ahead as the path moves:
		// held, it's where the path is).
		e1, a1 := tr.at(math.Min(t+followLookahead*rate, end))
		// A servo trails a moving goal a little (its position loop): add how
		// far behind the path it is to the goal, so it runs on the path.
		// At most followCatchUp: a rig held up far behind eases back onto
		// the path instead of lurching after it.
		pe, pa := tr.at(t)
		e1 += math.Max(-followCatchUp, math.Min(followCatchUp, pe-e0))
		a1 += math.Max(-followCatchUp, math.Min(followCatchUp, wrap180(pa-a0)))
		dt := math.Min(followLookahead, math.Max(end-t, followTick.Seconds()))
		speed := func(d float64) int { return max(20, int(math.Abs(d)/dt*stepsPerDegree)) }
		if err := r.moveToAt(&e1, &a1, speed(e1-e0), speed(wrap180(a1-a0))); err != nil {
			return err
		}
		// The next tick, or the next photo if that comes first.
		wait := followTick
		if rate > 0 {
			if d := time.Duration((tr.pointT[next] - vt) / rate * float64(time.Second)); d < wait {
				wait = max(0, d)
			}
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), r.stop())
		case <-time.After(wait):
		}
	}
}

const stepsPerDegree = 4096.0 / 360

// followCatchUp is the most (degrees) follow adds to a goal for the lag.
const followCatchUp = 2.0
