package main

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
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

// fakeApertureGphoto2: a camera whose f-number has 7 choices (f/1…f/32), on
// a lens that only goes f/2.8 to f/11: asked for more, it stops at the
// lens's limit (as the A6600 does).
const fakeApertureGphoto2 = `#!/bin/sh
case "$*" in
*--auto-detect*) echo "Fake Camera (PC Control)       usb:001,002"; exit 0;;
esac
set -- f/1 f/2 f/2.8 f/4 f/5.6 f/8 f/11 f/16 f/32
cur=3
prompt() { printf 'gphoto2: {%s} /> ' "$PWD"; }
prompt
while IFS= read -r line; do
  echo "$line"
  case "$line" in
  "get-config /main/capturesettings/f-number")
    echo "Label: F-Number"; echo "Readonly: 0"; echo "Type: RADIO"
    i=0; for v in f/1 f/2 f/2.8 f/4 f/5.6 f/8 f/11 f/16 f/32; do
      [ $i = $cur ] && echo "Current: $v"; i=$((i+1)); done
    i=0; for v in f/1 f/2 f/2.8 f/4 f/5.6 f/8 f/11 f/16 f/32; do echo "Choice: $i $v"; i=$((i+1)); done
    echo END;;
  "set-config-index /main/capturesettings/f-number="*)
    cur=${line##*=}; [ $cur -lt 2 ] && cur=2; [ $cur -gt 6 ] && cur=6;;
  get-config*) echo "Label: Other"; echo "Current: x"; echo END;;
  exit|quit) exit 0;;
  esac
  prompt
done
`

func TestGphoto2ProbeAperture(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gphoto2"), []byte(fakeApertureGphoto2), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cam, _, err := openCamera(gphoto2Prefix + "usb:001,002")
	if err != nil {
		t.Fatal(err)
	}
	defer cam.Close()
	aperture := func() cameraSetting {
		ss, err := cam.(settingsCamera).Settings()
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range ss {
			if s.Key == "aperture" {
				return s
			}
		}
		t.Fatal("no aperture")
		return cameraSetting{}
	}
	if a := aperture(); len(a.Choices) != 9 || !a.Probe || a.Probed {
		t.Fatalf("before probing: %+v", a)
	}
	if err := cam.(proberCamera).Probe("aperture"); err != nil {
		t.Fatal(err)
	}
	a := aperture()
	if !a.Probed || strings.Join(a.Choices, " ") != "f/2.8 f/4 f/5.6 f/8 f/11" {
		t.Fatalf("after probing: %+v, want the lens's f/2.8…f/11", a)
	}
	if a.Current != "f/4" {
		t.Fatalf("the aperture's at %s after probing, want it back at f/4", a.Current)
	}
}
