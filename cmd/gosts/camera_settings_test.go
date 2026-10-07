package main

import (
	"context"
	"fmt"
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

// fakeLaggyGphoto2: white balance takes a new value only some reads after
// it's set (the A6600 applies it a moment later), and never takes "Shade".
const fakeLaggyGphoto2 = `#!/bin/sh
case "$*" in
*--auto-detect*) echo "Fake Camera (PC Control)       usb:001,002"; exit 0;;
esac
cur=Automatic; want=""; lag=0
prompt() { printf 'gphoto2: {%s} /> ' "$PWD"; }
prompt
while IFS= read -r line; do
  echo "$line"
  case "$line" in
  "get-config /main/imgsettings/whitebalance")
    if [ -n "$want" ]; then lag=$((lag-1)); if [ $lag -le 0 ]; then cur=$want; want=""; fi; fi
    echo "Label: WhiteBalance"; echo "Readonly: 0"; echo "Current: $cur"
    echo "Choice: 0 Automatic"; echo "Choice: 1 Daylight"; echo "Choice: 2 Shade"; echo END;;
  "set-config-index /main/imgsettings/whitebalance="*)
    i=${line##*=}
    case $i in 0) want=Automatic;; 1) want=Daylight;; 2) want="";; esac
    lag=3;;
  get-config*) echo "Label: Other"; echo "Current: x"; echo END;;
  exit|quit) exit 0;;
  esac
  prompt
done
`

func TestGphoto2SetWaits(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gphoto2"), []byte(fakeLaggyGphoto2), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer func(d time.Duration) { settingWait = d }(settingWait)
	settingWait = 2 * time.Second
	cam, _, err := openCamera(gphoto2Prefix + "usb:001,002")
	if err != nil {
		t.Fatal(err)
	}
	defer cam.Close()
	sc := cam.(settingsCamera)
	if err := sc.Set("whitebalance", "Daylight"); err != nil {
		t.Fatal(err)
	}
	ss, _ := sc.Settings()
	for _, s := range ss {
		if s.Key == "whitebalance" && s.Current != "Daylight" {
			t.Fatalf("after Set: %s, want Daylight (Set returned before the camera took it)", s.Current)
		}
	}
	if err := sc.Set("whitebalance", "Shade"); err == nil {
		t.Fatal("no error for a value the camera never took")
	}
}

// Colour temperature: offered (as a number, 2500–9900 K by 100) only with
// white balance on Kelvin mode; values off the steps are refused.
func TestColorTemperature(t *testing.T) {
	c, err := parseGphoto2Config("Label: Color Temperature\nReadonly: 0\nType: RANGE\nCurrent: 3100\nBottom: 2500\nTop: 9900\nStep: 100\nEND\n")
	if err != nil || c.current != "3100" || c.bottom != 2500 || c.top != 9900 || c.step != 100 {
		t.Fatalf("%+v %v", c, err)
	}
	s := &simCamera{}
	has := func() (cameraSetting, bool) {
		ss, _ := s.Settings()
		for _, x := range ss {
			if x.Key == "colortemp" {
				return x, true
			}
		}
		return cameraSetting{}, false
	}
	if _, ok := has(); ok {
		t.Fatal("colour temperature offered outside Kelvin mode")
	}
	s.Set("whitebalance", kelvinMode)
	ct, ok := has()
	if !ok || !ct.Range || ct.Min != 2500 || ct.Max != 9900 || ct.Step != 100 {
		t.Fatalf("in Kelvin mode: %+v", ct)
	}
	if err := s.Set("colortemp", "4300"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"4350", "2000", "warm"} {
		if err := s.Set("colortemp", bad); err == nil {
			t.Errorf("%q taken", bad)
		}
	}
	ss, _ := s.Settings()
	if got := settingsCaption(ss); got != "1/60 · f/8 · ISO 200 · 4300K · focus: Manual" {
		t.Fatalf("caption %q", got)
	}
}

// TestWhiteBalanceShift: the A6600's A–B (a range by 2) and G–M (a menu)
// shifts, as the dialog has them: quarter steps towards B or M.
func TestWhiteBalanceShift(t *testing.T) {
	for _, c := range []struct {
		v    int
		ends []string
		want string
	}{{0, []string{"A", "B"}, "0"}, {-8, []string{"A", "B"}, "A2"}, {6, []string{"A", "B"}, "B1.5"}, {3, []string{"G", "M"}, "M0.75"}, {-28, []string{"G", "M"}, "G7"}} {
		if got := shiftLabel(c.v, c.ends); got != c.want {
			t.Errorf("shiftLabel(%d) = %q, want %q", c.v, got, c.want)
		}
	}
	ab, err := parseGphoto2Config("Label: AB Filter\nReadonly: 0\nType: RANGE\nCurrent: 200\nBottom: 164\nTop: 220\nStep: 2\nEND\n")
	if err != nil {
		t.Fatal(err)
	}
	st, ok := shiftSetting(ab, "abshift", "A–B", []string{"A", "B"})
	if !ok || st.Current != "-8" || st.Min != -28 || st.Max != 28 || st.Step != 2 {
		t.Errorf("A–B: %+v", st)
	}
	menu := "Label: CC Filter\nReadonly: 0\nType: MENU\nCurrent: 189\n"
	for i := 0; i <= 56; i++ {
		menu += fmt.Sprintf("Choice: %d %d\n", i, 164+i)
	}
	gm, err := parseGphoto2Config(menu + "END\n")
	if err != nil {
		t.Fatal(err)
	}
	st, ok = shiftSetting(gm, "gmshift", "G–M", []string{"G", "M"})
	if !ok || st.Current != "3" || st.Min != -28 || st.Max != 28 || st.Step != 1 {
		t.Errorf("G–M: %+v", st)
	}
	if _, err := checkShift("3", -28, 28, 2); err == nil {
		t.Error("A–B takes halves only")
	}

	s := &simCamera{}
	if err := s.Set("abshift", "-8"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("gmshift", "29"); err == nil {
		t.Error("G–M past 7 taken")
	}
	ss, _ := s.Settings()
	if c := settingsCaption(ss); !strings.Contains(c, "A2") {
		t.Errorf("caption %q", c)
	}
}

// TestSimFocus: the simulated lens starts out of focus, nudges move it
// (bigger steps further), autofocus makes it sharp.
func TestSimFocus(t *testing.T) {
	s := &simCamera{}
	b0 := s.blur()
	if b0 <= 0 {
		t.Fatalf("starts sharp: blur %v", b0)
	}
	if err := s.Nudge(-3); err != nil { // 4 towards near: from 9 to 5
		t.Fatal(err)
	}
	if b := s.blur(); b >= b0 {
		t.Errorf("nudged towards sharp, blur %v → %v", b0, b)
	}
	if err := s.Nudge(8); err == nil {
		t.Error("nudge 8 taken")
	}
	if ind, err := s.Autofocus(); err != nil || ind != "Focus Locked" || s.blur() != 0 {
		t.Errorf("autofocus: %q %v, blur %v", ind, err, s.blur())
	}
}
