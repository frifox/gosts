package main

import "testing"

func TestPlanShots(t *testing.T) {
	shots, err := planShots(Plan{Rings: 3, ElevationFrom: 0, ElevationTo: 60, PerRing: 4})
	if err != nil || len(shots) != 12 {
		t.Fatal(len(shots), err)
	}
	want := []struct{ e, a float64 }{
		{0, 0}, {0, 90}, {0, -180}, {0, -90}, // ring 1 round one way
		{30, -90}, {30, -180}, {30, 90}, {30, 0}, // ring 2 back, no full unwind
		{60, 0}, {60, 90}, {60, -180}, {60, -90},
	}
	for i, w := range want {
		if shots[i].Elevation != w.e || shots[i].Azimuth != w.a {
			t.Fatalf("shot %d: %+v, want %v", i, shots[i], w)
		}
	}
	if _, err := planShots(Plan{Rings: 0, PerRing: 4}); err == nil {
		t.Fatal("0 rings accepted")
	}
	if one, _ := planShots(Plan{Rings: 1, ElevationFrom: 20, ElevationTo: 80, PerRing: 2}); one[0].Elevation != 20 {
		t.Fatal("a single ring should be at the lowest elevation", one[0])
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
