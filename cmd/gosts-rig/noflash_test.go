package main

import (
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
