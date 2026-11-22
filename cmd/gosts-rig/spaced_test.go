package main

import "testing"

func TestPlanTidySpaced(t *testing.T) {
	p := Plan{Moving: true, Spaced: true, SpacedSec: -1}
	p.tidy()
	if p.Spaced || p.SpacedSec != 0 {
		t.Errorf("Continuous and Spaced: %+v, want Continuous alone, no negative spacing", p)
	}
	p = Plan{Spaced: true, SpacedSec: 3}
	p.tidy()
	if !p.Spaced || p.SpacedSec != 3 {
		t.Errorf("Spaced lost: %+v", p)
	}
}

func TestSpacedGap(t *testing.T) {
	p := Plan{Spaced: true, SpacedSec: 3}
	if g := spacedGap(Motion{SpeedBy: SpeedByRate, SecPerPhoto: 5}, p); g != 3 {
		t.Errorf("by the servos' speed: %g s, want the spacing, 3", g)
	}
	if g := spacedGap(Motion{SpeedBy: SpeedByPhoto, SecPerPhoto: 5}, p); g != 5 {
		t.Errorf("by the seconds a photo (5): %g s, want 5", g)
	}
	if g := spacedGap(Motion{SpeedBy: SpeedByPhoto, SecPerPhoto: 1}, p); g != 3 {
		t.Errorf("by the seconds a photo (1): %g s, want the spacing, 3", g)
	}
}
