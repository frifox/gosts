package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// resumeApp is an app on the simulators, its captures in photos.
func resumeApp(t *testing.T, cfgPath string) *app {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{cfg: cfg, clients: map[*client]struct{}{}}
	a.rig = &rig{cfg: cfg, out: func(any) {}}
	if err := a.rig.connect(context.Background(), simPort, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.rig.disconnect)
	a.camera = &cameraConn{out: func(any) {}, cfg: cfg}
	if err := a.camera.connect(simCameraID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.camera.disconnect)
	a.cap = &capture{rig: a.rig, camera: a.camera, out: func(any) {}}
	a.model = &modelBuilder{out: func(any) {}}
	a.camera.onPicture = a.cap.pictured
	return a
}

func waitCapture(t *testing.T, c *capture) {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); c.isRunning(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the capture is still running")
		}
	}
}

// A capture stopped part way keeps its plan and shots (capture.json) and its
// alignment data for the photos taken; after a restart, opening it puts its
// plan back and the shots taken first (Before); Resume takes the rest, and
// it's complete, its alignment data for all.
func TestCaptureResume(t *testing.T) {
	defer func(d time.Duration) { simPictureWait = d }(simPictureWait)
	simPictureWait = 10 * time.Millisecond // (no page rendering them: made-up pictures, at once)
	oldPhotos := photoDir
	photoDir = t.TempDir()
	defer func() { photoDir = oldPhotos; newBatch() }()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	plan := Plan{Photos: 6, SettleMS: 0, ExportFor: ExportMetashape}

	a := resumeApp(t, cfgPath)
	startBatch(true)
	name := currentBatch()
	if err := a.cap.start(plan); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); a.cap.msg().Index < 3; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("3 photos not taken")
		}
	}
	a.cap.stop()
	waitCapture(t, a.cap)
	dir := filepath.Join(photoDir, name)
	s, err := readCaptureState(dir)
	if err != nil {
		t.Fatal(err)
	}
	taken := s.taken()
	if !s.resumable() || taken < 3 || taken == 6 {
		t.Fatalf("after Stop: %d of %d taken, complete %v", taken, len(s.Shots), s.Complete)
	}
	if m := a.cap.msg(); !m.Resumable || m.Index != taken {
		t.Fatalf("after Stop: resumable %v, index %d", m.Resumable, m.Index)
	}
	csv := func() int {
		b, _ := os.ReadFile(filepath.Join(dir, "alignment-metashape.csv"))
		return strings.Count(string(b), ".JPG")
	}
	if n := csv(); n != taken {
		t.Fatalf("alignment data for %d photos, want %d", n, taken)
	}

	// A restart: a new app, a new current capture; the interrupted one opened.
	a.camera.disconnect()
	a.rig.disconnect()
	b := resumeApp(t, cfgPath)
	startBatch(false)
	b.cfg.update(func(c *Config) { c.Plan = Plan{Photos: 99} }) // (another plan since)
	bs, _ := listBatches()
	if len(bs) != 1 || !bs[0].Interrupted || bs[0].Taken != taken || bs[0].Planned != 6 {
		t.Fatalf("Browse: %+v", bs)
	}
	if err := b.adopt(name); err != nil {
		t.Fatal(err)
	}
	if b.cfg.get().Plan.Photos != 6 || currentBatch() != name {
		t.Fatalf("plan %d photos, current %s", b.cfg.get().Plan.Photos, currentBatch())
	}
	m := b.cap.msg()
	if !m.Resumable || m.Index != taken || !m.Shots[0].Before || m.Shots[taken].Done {
		t.Fatalf("opened: resumable %v, index %d", m.Resumable, m.Index)
	}
	if err := b.cap.start(b.cfg.get().Plan); err != nil {
		t.Fatal(err)
	}
	waitCapture(t, b.cap)
	s, _ = readCaptureState(dir)
	if !s.Complete || s.taken() != 6 || b.cap.msg().Resumable {
		t.Fatalf("resumed: complete %v, %d taken", s.Complete, s.taken())
	}
	if n := csv(); n != 6 {
		t.Fatalf("alignment data for %d photos, want 6", n)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "SIM_*.JPG"))
	if len(files) != 6 {
		t.Fatalf("%d photos in the folder, want 6: %v", len(files), files)
	}
}
