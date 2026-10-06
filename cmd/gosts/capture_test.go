package main

import (
	"math"
	"testing"

	"github.com/frifox/gosts"
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
	tr := newTrajectory(e, a, 50, 100)
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
