package main

import (
	"bytes"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// grayJPEG is a small JPEG all of brightness lum.
func grayJPEG(lum uint8) []byte {
	img := image.NewGray(image.Rect(0, 0, 32, 24))
	for i := range img.Pix {
		img.Pix[i] = lum
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

// flashCapture is a capture running (no rig moves: its logic alone) with
// 5 shots, the first n taken (bright photos 1..n, files in its folder),
// watching for no-flash photos; the Trash a test one.
func flashCapture(t *testing.T, n int) (*capture, string) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.Mkdir(filepath.Join(home, ".Trash"), 0o755)
	dir := t.TempDir()
	cfg, _ := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	cfg.update(func(c *Config) { c.Cooldown = Cooldown{Dark: true, DarkMinutes: 1, Shots: 50, Minutes: 2} })
	cam := &cameraConn{out: func(any) {}, cfg: cfg, photos: map[int]photo{}}
	c := &capture{rig: &rig{cfg: cfg, out: func(any) {}}, camera: cam, out: func(any) {},
		shots: make([]shot, 5), running: true, folder: dir, photoShot: map[int]int{}, resume: make(chan struct{})}
	for i := 0; i < n; i++ {
		c.add(t, i+1, i, 150)
	}
	return c, dir
}

// add: photo k (brightness lum) came over for shot i, its file saved.
func (c *capture) add(t *testing.T, k, i int, lum uint8) photo {
	name := filepath.Join(c.folder, "p"+string(rune('0'+k))+".jpg")
	jpg := grayJPEG(lum)
	if err := os.WriteFile(name, jpg, 0o644); err != nil {
		t.Fatal(err)
	}
	p := photo{JPEG: jpg, Files: []string{name}, At: time.Now()}
	c.camera.mu.Lock()
	c.camera.photos[k] = p
	c.camera.mu.Unlock()
	c.mu.Lock()
	c.photoShot[k] = i
	c.shots[i].Done, c.shots[i].File = true, filepath.Base(name)
	c.index = i + 1
	c.mu.Unlock()
	c.checkFlash(k, i, p)
	return p
}

// From the second photo on, a much darker one is caught: the capture goes
// back to its shot and pauses; the photo's kept while it cools, and deleted
// (Trash, timeline) just before it carries on.
func TestNoFlash(t *testing.T) {
	c, dir := flashCapture(t, 1)
	c.add(t, 2, 1, 15) // (the flash didn't fire)
	m := c.msg()
	if m.Index != 1 || m.Shots[1].Done || m.CoolUntil == 0 {
		t.Fatalf("index %d, shot 2 done %v, cooling %v: want back at shot 2, paused", m.Index, m.Shots[1].Done, m.CoolUntil)
	}
	if m.DarkEvery != 1 || m.DarkSince != 0 {
		t.Errorf("the flash lasted %v photos, %d since: want 1 (the one before), 0", m.DarkEvery, m.DarkSince)
	}
	if _, err := os.Stat(filepath.Join(dir, "p2.jpg")); err != nil {
		t.Fatal("the dark photo's gone while cooling")
	}
	if err := c.pause(false); err != nil { // (the cooldown over: on again)
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "p2.jpg")); err == nil {
		t.Fatal("the dark photo's still there as it carries on")
	}
	if _, ok := c.camera.photo(2); ok {
		t.Fatal("the dark photo's still on the timeline")
	}
	c.add(t, 3, 1, 20) // (taken again, and no flash again: caught too)
	if m := c.msg(); m.Index != 1 || m.CoolUntil == 0 {
		t.Fatalf("the retake's dark too: index %d, cooling %v", m.Index, m.CoolUntil)
	}
}

// Resumed, a capture's new photos are compared with its photos from before
// (seen: one flash photo, then a dark one, wasn't caught).
func TestNoFlashSeededOnResume(t *testing.T) {
	c, _ := flashCapture(t, 2)
	c.bright = nil // (a new run)
	c.seedBright()
	if len(c.bright) != 2 {
		t.Fatalf("seeded %v, want the 2 photos before", c.bright)
	}
	c.add(t, 3, 2, 15)
	if m := c.msg(); m.Index != 2 || m.CoolUntil == 0 {
		t.Fatalf("index %d, cooling %v: the first dark photo after resuming wasn't caught", m.Index, m.CoolUntil)
	}
}

// TestDarkPauses: the no-flash pauses still to come, the flash lasting so
// many photos (its runs' average, each new run counted in).
func TestDarkPauses(t *testing.T) {
	for _, tc := range []struct {
		left  int
		every float64
		since int
		want  float64
	}{
		{100, 0, 0, 0},   // none dark yet: none counted
		{30, 40, 0, 0},   // all within the next run
		{40, 40, 0, 0},   // exactly
		{41, 40, 0, 1},   // one more: a pause
		{100, 40, 0, 2},  // 40, pause, 40, pause, 20
		{100, 40, 30, 3}, // 10, pause, 40, pause, 40, pause, 10
		{100, 40, 50, 3}, // past the average already: the next one now
	} {
		if got := darkPauses(tc.left, tc.every, tc.since); got != tc.want {
			t.Errorf("darkPauses(%d, %v, %d) = %v, want %v", tc.left, tc.every, tc.since, got, tc.want)
		}
	}
	c := &capture{}
	if c.darkEvery() != 0 {
		t.Error("no runs: not 0")
	}
	c.darkRuns = []int{60} // (a fluke, say)
	if c.darkEvery() != 60 {
		t.Errorf("one run: %v", c.darkEvery())
	}
	c.darkRuns = append(c.darkRuns, 30, 30)
	if c.darkEvery() != 40 {
		t.Errorf("three runs: %v, want their average, 40", c.darkEvery())
	}
}
