package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Auto-pause after a no-flash photo: photo 5 comes out dark (the flash
// missed once): it's set aside (no-flash/), the capture pauses
// (DarkMinutes), then takes shot 5 again; all 8 shots end up with bright
// photos, 9 taken.
func TestNoFlashPause(t *testing.T) {
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
	cam.cam = &flashCam{fires: 100, hiccup: 5}
	c := &capture{rig: r, camera: cam, out: func(any) {}}
	cam.onPicture = c.pictured
	startBatch(true)
	if err := c.start(Plan{Photos: 8, SettleMS: 0}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(60 * time.Second); c.isRunning(); time.Sleep(20 * time.Millisecond) {
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
		if sh.File == "f005.jpg" {
			t.Fatal("the dark photo is a shot's")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, noFlashDir, "f005.jpg")); err != nil {
		t.Fatal("the dark photo isn't set aside:", err)
	}
	if n := cam.cam.(*flashCam).n; n != 9 {
		t.Fatalf("%d photos taken, want 9 (shot 5 again)", n)
	}
}
