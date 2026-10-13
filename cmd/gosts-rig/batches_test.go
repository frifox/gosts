package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Open Capture lists the batch folders (not the samples', not empty ones),
// newest first, each with its middle photo; a RAW counts only without its
// JPEG.
func TestListBatches(t *testing.T) {
	old := photoDir
	photoDir = t.TempDir()
	defer func() { photoDir = old }()
	put := func(dir string, files ...string) {
		os.MkdirAll(filepath.Join(photoDir, dir), 0o755)
		for _, f := range files {
			os.WriteFile(filepath.Join(photoDir, dir, f), []byte("x"), 0o644)
		}
	}
	put("2026-01-01 10.00.00", "DSC1.JPG", "DSC1.ARW", "DSC2.JPG", "DSC3.ARW", "notes.txt")
	put("2026-01-02 10.00.00", "a.jpg")
	put("2026-01-02 10.00.00-samples", "s.jpg")
	put("empty")
	bs, err := listBatches()
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 2 || bs[0].Name != "2026-01-02 10.00.00" || bs[1].Count != 3 || bs[1].Mid != "DSC2.JPG" {
		t.Fatalf("batches %+v", bs)
	}
	for _, n := range []string{"..", "../x", ".hidden", "a/b", ""} {
		if cleanName(n) {
			t.Errorf("cleanName(%q) true", n)
		}
	}
}

// Renaming a capture takes its samples along; the current batch follows it;
// a taken name or a bad one is refused.
func TestRenameBatch(t *testing.T) {
	old := photoDir
	photoDir = t.TempDir()
	defer func() { photoDir = old }()
	for _, d := range []string{"b1", "b1-samples", "b2"} {
		os.MkdirAll(filepath.Join(photoDir, d), 0o755)
	}
	batch.Lock()
	batch.dir = filepath.Join(photoDir, "b1")
	batch.Unlock()
	defer newBatch()
	a := &app{}
	if err := a.renameBatch("b1", "b2"); err == nil {
		t.Fatal("renamed onto b2")
	}
	for _, bad := range []string{"../x", ".h", "x-samples", "a/b", "a:b", " "} {
		if err := a.renameBatch("b1", bad); err == nil {
			t.Errorf("renamed to %q", bad)
		}
	}
	if err := a.renameBatch("b1", " Benchy "); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"Benchy", "Benchy-samples"} {
		if _, err := os.Stat(filepath.Join(photoDir, d)); err != nil {
			t.Error(err)
		}
	}
	if batchFolder() != filepath.Join(photoDir, "Benchy") {
		t.Errorf("current batch %s", batchFolder())
	}
}

// Deleting goes to the Trash: a photo with its RAW (others stay), a name
// taken there gets another; a capture with its samples.
func TestDeleteToTrash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".Trash"), 0o755)
	old := photoDir
	photoDir = t.TempDir()
	defer func() { photoDir = old }()
	put := func(dir string, files ...string) {
		os.MkdirAll(filepath.Join(photoDir, dir), 0o755)
		for _, f := range files {
			os.WriteFile(filepath.Join(photoDir, dir, f), []byte("x"), 0o644)
		}
	}
	put("b1", "DSC1.JPG", "DSC1.ARW", "DSC2.JPG")
	put("b1-samples", "s.jpg")
	os.WriteFile(filepath.Join(home, ".Trash", "DSC1.JPG"), []byte("older"), 0o644)
	if err := trashPhoto(filepath.Join(photoDir, "b1", "DSC1.JPG")); err != nil {
		t.Fatal(err)
	}
	ps, _ := batchPhotos(filepath.Join(photoDir, "b1"))
	if len(ps) != 1 || ps[0].File != "DSC2.JPG" {
		t.Fatalf("left: %+v", ps)
	}
	for _, f := range []string{"DSC1.JPG", "DSC1 2.JPG", "DSC1.ARW"} {
		if _, err := os.Stat(filepath.Join(home, ".Trash", f)); err != nil {
			t.Errorf("not in the Trash: %s", f)
		}
	}
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	a := &app{cfg: cfg, camera: &cameraConn{out: func(any) {}}}
	a.cap = &capture{camera: a.camera, out: func(any) {}}
	if err := a.deleteBatch("b1"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"b1", "b1-samples"} {
		if _, err := os.Stat(filepath.Join(photoDir, d)); !os.IsNotExist(err) {
			t.Errorf("%s still there", d)
		}
		if _, err := os.Stat(filepath.Join(home, ".Trash", d)); err != nil {
			t.Errorf("%s not in the Trash", d)
		}
	}
	if err := a.deleteBatch("../x"); err == nil {
		t.Error("deleted outside the photos")
	}
}

// Renaming the current capture before its folder's made (nothing saved
// yet): its name changes, the folder comes with the first photo.
func TestRenameBatchNotMade(t *testing.T) {
	old := photoDir
	photoDir = t.TempDir()
	defer func() { photoDir = old; newBatch() }()
	startBatch(false)
	from := currentBatch()
	a := &app{}
	if err := a.renameBatch(from, "Benchy"); err != nil {
		t.Fatal(err)
	}
	if currentBatch() != "Benchy" {
		t.Fatalf("current %q, want Benchy", currentBatch())
	}
	if _, err := os.Stat(filepath.Join(photoDir, "Benchy")); !os.IsNotExist(err) {
		t.Error("the folder's made before anything's saved")
	}
	if batchFolder() != filepath.Join(photoDir, "Benchy") {
		t.Errorf("photos go to %s", batchFolder())
	}
	if err := a.renameBatch("not-there", "x"); err == nil {
		t.Error("renamed a capture that isn't there")
	}
}

// Renaming the current capture: the timeline's files follow it (deleting
// one still finds it); refused while a photo's coming over, or the
// alignment data's being written.
func TestRenameCurrentBusy(t *testing.T) {
	old := photoDir
	photoDir = t.TempDir()
	defer func() { photoDir = old; newBatch() }()
	startBatch(true)
	dir := batchFolder()
	os.MkdirAll(filepath.Join(dir, "samples"), 0o755)
	f := filepath.Join(dir, "DSC1.JPG")
	os.WriteFile(f, []byte("x"), 0o644)
	cam := &cameraConn{out: func(any) {}}
	cam.photos = map[int]photo{1: {Files: []string{f}}, 2: {Files: []string{filepath.Join(dir, "samples", "s.JPG")}}}
	a := &app{camera: cam}
	a.cap = &capture{camera: cam, out: func(any) {}}

	cam.inFlight = map[int]bool{3: true}
	if err := a.renameBatch(filepath.Base(dir), "Benchy"); err == nil {
		t.Error("renamed with a photo coming over")
	}
	cam.inFlight = nil
	a.cap.exporting.Add(1)
	if err := a.renameBatch(filepath.Base(dir), "Benchy"); err == nil {
		t.Error("renamed with the alignment data being written")
	}
	a.cap.exporting.Add(-1)

	if err := a.renameBatch(filepath.Base(dir), "Benchy"); err != nil {
		t.Fatal(err)
	}
	nd := filepath.Join(photoDir, "Benchy")
	if got := cam.photos[1].Files[0]; got != filepath.Join(nd, "DSC1.JPG") {
		t.Errorf("photo 1's file %s, want in Benchy", got)
	}
	if got := cam.photos[2].Files[0]; got != filepath.Join(nd, "samples", "s.JPG") {
		t.Errorf("sample's file %s, want in Benchy/samples", got)
	}
}
