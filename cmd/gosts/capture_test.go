package main

import (
	"math"
	"testing"
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
