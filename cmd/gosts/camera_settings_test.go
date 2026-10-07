package main

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestParseGphoto2Config(t *testing.T) {
	// As the A6600's shell prints it (with a redrawn prompt in front).
	out := "\r<07311} /> get-config /main/capturesettings/f-number\nget-config /main/capturesettings/f-number\n" +
		"Label: F-Number\nReadonly: 0\nType: RADIO\nCurrent: f/4\nChoice: 0 f/1\nChoice: 1 f/4\nChoice: 2 f/8\nEND\n"
	c, err := parseGphoto2Config(out)
	if err != nil {
		t.Fatal(err)
	}
	if c.label != "F-Number" || c.current != "f/4" || c.readOnly || len(c.choices) != 3 || c.choices[2] != "f/8" {
		t.Fatalf("%+v", c)
	}
	wb, _ := parseGphoto2Config("Label: WhiteBalance\nReadonly: 0\nCurrent: Automatic\nChoice: 5 Fluorescent: Warm White\n")
	if wb.choices[0] != "Fluorescent: Warm White" {
		t.Fatalf("choice with spaces: %q", wb.choices)
	}
	if _, err := parseGphoto2Config("*** Error: not found"); err == nil {
		t.Fatal("no error for a missing setting")
	}
	if keepChoice("iso", "200 Multi Frame Noise Reduction") || !keepChoice("iso", "200") || keepChoice("mode", "Movie (P)") {
		t.Fatal("keepChoice")
	}
}

func TestSimCameraSettings(t *testing.T) {
	s := &simCamera{}
	ss, _ := s.Settings()
	if settingsCaption(ss) != "1/60 · f/8 · ISO 200 · Automatic · focus: Manual" {
		t.Fatalf("caption %q", settingsCaption(ss))
	}
	if e := s.exposure(); math.Abs(e-1) > 1e-9 {
		t.Fatalf("exposure at the reference %v", e)
	}
	s.Set("shutter", "1/30") // twice the light
	s.Set("aperture", "f/5.6")
	if e := s.exposure(); math.Abs(e-4.08) > 0.05 { // 2 × (8/5.6)² ≈ 4.08
		t.Fatalf("exposure %v", e)
	}
	if err := s.Set("iso", "123"); err == nil {
		t.Fatal("a value that isn't a choice")
	}
	s.Set("mode", "A") // the camera evens it out
	if e := s.exposure(); e != 1 {
		t.Fatalf("exposure in A %v", e)
	}
}

// A sample shot comes to the page as a sample, captioned, and stays off the
// timeline.
func TestSampleShot(t *testing.T) {
	simPictureWait = 50 * time.Millisecond
	defer func() { simPictureWait = 2 * time.Second }()
	var got []photoMsg
	c := &cameraConn{out: func(m any) {
		if p, ok := m.(photoMsg); ok {
			got = append(got, p)
		}
	}}
	c.connect(simCameraID)
	if err := c.sample(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if len(got) != 1 || !got[0].Sample || got[0].Caption == "" {
		t.Fatalf("got %+v", got)
	}
	if tl := c.timeline().Photos; len(tl) != 0 {
		t.Fatalf("a sample on the timeline: %v", tl)
	}
	if _, ok := c.photo(got[0].N); !ok {
		t.Fatal("the sample's picture isn't served")
	}
}
