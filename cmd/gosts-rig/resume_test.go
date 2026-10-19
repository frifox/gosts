package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A capture's state (capture.json) and alignment data are saved as it goes;
// an interrupted one, opened after a restart, has its plan back, the shots
// taken first (Before) and their photos on the timeline. A photo deleted
// since (its file gone) leaves its shot to be taken again, not a broken one.
func TestCaptureResume(t *testing.T) {
	oldPhotos := photoDir
	photoDir = t.TempDir()
	defer func() { photoDir = oldPhotos; newBatch() }()
	name := "interrupted"
	dir := filepath.Join(photoDir, name)
	os.MkdirAll(dir, 0o755)
	plan := Plan{Photos: 4, ExportFor: ExportMetashape}
	shots, _, _, _ := planShots(plan, defaultConfig().Motion)
	e, a := 10.0, 20.0
	for i := 0; i < 3; i++ { // 3 of 4 taken; photo 2 deleted since
		shots[i].File, shots[i].Done, shots[i].ActualElevation, shots[i].ActualAzimuth = fmt.Sprintf("p%d.jpg", i+1), true, &e, &a
		if i != 1 {
			os.WriteFile(filepath.Join(dir, shots[i].File), grayJPEG(150), 0o644)
		}
	}
	cfg, _ := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	cam := &cameraConn{out: func(any) {}, cfg: cfg}
	c := &capture{rig: &rig{cfg: cfg, out: func(any) {}}, camera: cam, out: func(any) {}, folder: dir, plan: plan, shots: shots}
	c.saveNow()
	if b, _ := os.ReadFile(filepath.Join(dir, "alignment-metashape.csv")); strings.Count(string(b), ".jpg") != 2 {
		t.Fatalf("alignment data:\n%s\nwant the 2 photos there", b)
	}
	if s, err := readCaptureState(dir); err != nil || !s.resumable() || len(s.Shots) != 4 {
		t.Fatalf("capture.json: %v", err)
	}

	// After a restart: another plan since, a new app.
	cfg.update(func(c *Config) { c.Plan = Plan{Photos: 99} })
	app := &app{cfg: cfg, clients: map[*client]struct{}{}, camera: cam, model: &modelBuilder{out: func(any) {}}}
	app.rig = &rig{cfg: cfg, out: func(any) {}}
	app.cap = &capture{rig: app.rig, camera: cam, out: func(any) {}}
	if bs, _ := listFolder(""); len(bs) != 1 || !bs[0].Interrupted || bs[0].Planned != 4 {
		t.Fatalf("Browse: %+v", bs)
	}
	if err := app.adopt(name); err != nil {
		t.Fatal(err)
	}
	m := app.cap.msg()
	if cfg.get().Plan.Photos != 4 || currentBatch() != name || !m.Resumable {
		t.Fatalf("plan %d photos, current %s, resumable %v", cfg.get().Plan.Photos, currentBatch(), m.Resumable)
	}
	if m.Index != 2 || !m.Shots[0].Before || !m.Shots[1].Before || m.Shots[2].Done {
		t.Fatalf("index %d (want 2: photo 2 deleted, its shot to take again)", m.Index)
	}
	if tl := cam.timeline(); len(tl.Photos) != 2 {
		t.Fatalf("timeline: %d photos, want the 2 there", len(tl.Photos))
	}
	if p, ok := cam.pictureOf(1); !ok || len(p.JPEG) == 0 {
		t.Fatal("photo 1's picture: not read from its file")
	}
}
