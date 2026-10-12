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
