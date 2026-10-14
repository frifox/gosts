package main

import (
	"fmt"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Auto-pause after a no-flash photo: photo 5 comes out dark (the flash
// missed), and so does its first retake (6): each is deleted (to the Trash,
// off the timeline), the capture pauses (DarkMinutes) and takes the shot
// again; all 8 shots end up with bright photos, 10 taken.
func TestNoFlashPause(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // (the deleted photos: its Trash, not the user's)
	os.Mkdir(filepath.Join(home, ".Trash"), 0o755)
	oldPhotos := photoDir
	photoDir = t.TempDir()
	defer func() { photoDir = oldPhotos; newBatch() }()
	cfg, _ := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	cfg.update(func(c *Config) { c.Cooldown = Cooldown{Dark: true, DarkMinutes: 0.002, Shots: 50, Minutes: 2} }) // (0.12 s)
	r := &rig{cfg: cfg, out: func(any) {}}
	if err := r.connect(context.Background(), simPort, 0); err != nil {
		t.Fatal(err)
	}
	defer r.disconnect()
	cam := &cameraConn{out: func(any) {}, cfg: cfg}
	cam.cam = &flashCam{fires: 100, hiccup: 5, darks: []int{6}}
	c := &capture{rig: r, camera: cam, out: func(any) {}}
	cam.onPicture = c.pictured
	startBatch(true)
	if err := c.start(Plan{Photos: 8, SettleMS: 0}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(90 * time.Second); c.isRunning(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("still running")
		}
	}
	dir := filepath.Join(photoDir, currentBatch())
	s, err := readCaptureState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Complete || s.taken() != 8 {
		t.Fatalf("complete %v, %d of 8 taken", s.Complete, s.taken())
	}
	for _, sh := range s.Shots {
		if sh.File == "f005.jpg" || sh.File == "f006.jpg" {
			t.Fatalf("a dark photo is a shot's: %s", sh.File)
		}
	}
	for _, f := range []string{"f005.jpg", "f006.jpg"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s is still in the capture", f)
		}
		if _, err := os.Stat(filepath.Join(home, ".Trash", f)); err != nil {
			t.Errorf("%s isn't in the Trash: %v", f, err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jpg"))
	if len(files) != 8 {
		t.Errorf("%d photos in the capture, want 8: %v", len(files), files)
	}
	if tl := cam.timeline(); len(tl.Photos) != 8 {
		t.Errorf("%d photos on the timeline, want 8", len(tl.Photos))
	}
	if n := cam.cam.(*flashCam).n; n != 10 {
		t.Fatalf("%d photos taken, want 10 (shot 5 twice more)", n)
	}
}

// noFlashRig is a rig on the simulator and a flashCam, its capture watching
// for no-flash photos (pausing 0.12 s), the Trash a test one.
func noFlashRig(t *testing.T, fc *flashCam) *capture {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.Mkdir(filepath.Join(home, ".Trash"), 0o755)
	oldPhotos := photoDir
	photoDir = t.TempDir()
	t.Cleanup(func() { photoDir = oldPhotos; newBatch() })
	cfg, _ := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	cfg.update(func(c *Config) { c.Cooldown = Cooldown{Dark: true, DarkMinutes: 0.002, Shots: 50, Minutes: 2} })
	r := &rig{cfg: cfg, out: func(any) {}}
	if err := r.connect(context.Background(), simPort, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.disconnect)
	cam := &cameraConn{out: func(any) {}, cfg: cfg}
	cam.cam = fc
	c := &capture{rig: r, camera: cam, out: func(any) {}}
	cam.onPicture = c.pictured
	startBatch(true)
	return c
}

func waitDone(t *testing.T, c *capture) {
	t.Helper()
	for deadline := time.Now().Add(90 * time.Second); c.isRunning(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("still running")
		}
	}
}

// The second photo already is compared with the first: dark, it's caught.
func TestNoFlashSecondPhoto(t *testing.T) {
	fc := &flashCam{fires: 100, hiccup: 2}
	c := noFlashRig(t, fc)
	if err := c.start(Plan{Photos: 4, SettleMS: 0}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, c)
	s, _ := readCaptureState(c.folder)
	if !s.Complete || fc.n != 5 {
		t.Fatalf("complete %v, %d taken (want 5: photo 2 again)", s.Complete, fc.n)
	}
}

// Resumed, the first photo is compared with the capture's photos from
// before: one flash photo, then a dark one, is caught (seen: it wasn't).
func TestNoFlashAfterResume(t *testing.T) {
	fc := &flashCam{fires: 100}
	c := noFlashRig(t, fc)
	if err := c.start(Plan{Photos: 6, SettleMS: 0}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); c.msg().Index < 3; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("3 photos not taken")
		}
	}
	c.stop()
	waitDone(t, c)
	if !c.msg().Resumable {
		t.Fatal("not resumable")
	}
	fc.mu.Lock()
	fc.hiccup = fc.n + 2 // (resumed: one with the flash, then one without)
	fc.mu.Unlock()
	if err := c.start(Plan{}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, c)
	s, _ := readCaptureState(c.folder)
	if !s.Complete || s.taken() != 6 {
		t.Fatalf("complete %v, %d of 6 taken", s.Complete, s.taken())
	}
	if fc.n != 7 {
		t.Fatalf("%d photos taken, want 7 (6, and the dark one taken again)", fc.n)
	}
	dark := fmt.Sprintf("f%03d.jpg", fc.hiccup)
	for _, sh := range s.Shots {
		if sh.File == dark {
			t.Fatalf("the dark photo (%s) is a shot's", dark)
		}
	}
	if _, err := os.Stat(filepath.Join(c.folder, dark)); err == nil {
		t.Fatalf("the dark photo (%s) is still in the capture", dark)
	}
}

// The dark photo stays (folder and timeline) while the capture cools, and
// is deleted just before it carries on.
func TestNoFlashKeptWhileCooling(t *testing.T) {
	fc := &flashCam{fires: 100, hiccup: 3}
	c := noFlashRig(t, fc)
	c.rig.cfg.update(func(cf *Config) { cf.Cooldown.DarkMinutes = 0.05 }) // (3 s)
	if err := c.start(Plan{Photos: 5, SettleMS: 0}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); c.msg().CoolUntil == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no pause")
		}
	}
	dark := filepath.Join(c.folder, "f003.jpg")
	if _, err := os.Stat(dark); err != nil {
		t.Fatal("the dark photo's gone while cooling:", err)
	}
	if _, ok := c.camera.photo(3); !ok {
		t.Fatal("the dark photo's off the timeline while cooling")
	}
	waitDone(t, c)
	if _, err := os.Stat(dark); err == nil {
		t.Fatal("the dark photo's still there after")
	}
	if _, ok := c.camera.photo(3); ok {
		t.Fatal("the dark photo's still on the timeline after")
	}
	if s, _ := readCaptureState(c.folder); !s.Complete || s.taken() != 5 {
		t.Fatalf("complete %v, %d of 5 taken", s.Complete, s.taken())
	}
}
