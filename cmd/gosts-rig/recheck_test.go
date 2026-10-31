package main

import (
	"os"
	"path/filepath"
	"testing"
)

// An interrupted capture, opened to resume, a photo deleted after (the
// timeline's bin, Browse, the Finder): its shot's taken again on Resume
// (seen: Resume carried on past it).
func TestRecheckDeleted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	cfg, _ := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	cam := &cameraConn{out: func(any) {}, cfg: cfg, photos: map[int]photo{}}
	c := &capture{rig: &rig{cfg: cfg, out: func(any) {}}, camera: cam, out: func(any) {}, folder: dir, resumable: true}
	for i, f := range []string{"capt_001.jpg", "capt_002.jpg", "capt_003.jpg", ""} {
		if f != "" {
			os.WriteFile(filepath.Join(dir, f), grayJPEG(100), 0o644)
		}
		c.shots = append(c.shots, shot{Num: i + 1, File: f, Done: f != ""})
	}
	c.index = 3
	c.recheck()
	if c.index != 3 {
		t.Fatalf("none deleted: index %d, want 3", c.index)
	}
	os.Remove(filepath.Join(dir, "capt_002.jpg"))
	c.recheck()
	if c.index != 2 {
		t.Fatalf("one deleted: %d taken, want 2", c.index)
	}
	var left []int
	for _, s := range c.shots[c.index:] {
		left = append(left, s.Num)
	}
	if len(left) != 2 || left[0] != 2 || left[1] != 4 {
		t.Errorf("to take on Resume: %v, want [2 4] (the deleted one's shot, in its order)", left)
	}
	if _, err := readCaptureState(dir); err != nil {
		t.Errorf("not saved: %v", err)
	}
	c.running = true // (running: its own photos' deletes are the capture's to handle)
	os.Remove(filepath.Join(dir, "capt_001.jpg"))
	c.recheck()
	if c.index != 2 {
		t.Errorf("running: index %d, changed", c.index)
	}
}
