package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A capture's photos are named after their place in its plan (capt_003.jpg,
// its RAW too), whatever the camera called them; a shot taken again
// replaces the one before (to the Trash); a photo not the capture's (Take
// Photo) keeps the camera's name.
func TestCapturePhotoNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.Mkdir(filepath.Join(home, ".Trash"), 0o755)
	dir := t.TempDir()
	cam := &cameraConn{out: func(any) {}, count: 9}
	c := &capture{camera: cam, out: func(any) {}, shots: make([]shot, 12), photoShot: map[int]int{}, folder: dir}
	for i := range c.shots {
		c.shots[i].Num = i + 1
	}
	cam.nameFor = c.fileStem
	got := func(n int, files ...string) []string {
		var paths []string
		for _, f := range files {
			p := filepath.Join(dir, f)
			os.WriteFile(p, []byte(f), 0o644)
			paths = append(paths, p)
		}
		cam.mu.Lock()
		cam.inFlight = map[int]bool{n: true}
		cam.mu.Unlock()
		cam.picture(n, photo{JPEG: []byte("x"), Files: paths, At: time.Now()})
		p, _ := cam.photo(n)
		return p.Files
	}
	c.photoShot[7] = 2 // (photo 7: the plan's 3rd shot)
	if f := got(7, "capt_DSC09999.JPG", "capt_DSC09999.ARW"); filepath.Base(f[0]) != "capt_003.jpg" || filepath.Base(f[1]) != "capt_003.arw" {
		t.Fatalf("named %v, want capt_003.jpg, capt_003.arw", f)
	}
	c.photoShot[8] = 2 // (taken again)
	if f := got(8, "capt_DSC00001.JPG"); filepath.Base(f[0]) != "capt_003.jpg" {
		t.Fatalf("named %v", f)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "capt_003.jpg")); string(b) != "capt_DSC00001.JPG" {
		t.Fatalf("capt_003.jpg is %q, want the photo taken again", b)
	}
	if _, err := os.Stat(filepath.Join(home, ".Trash", "capt_003.jpg")); err != nil {
		t.Fatal("the photo before isn't in the Trash")
	}
	if f := got(9, "capt_DSC00002.JPG"); filepath.Base(f[0]) != "capt_DSC00002.JPG" { // (not the capture's)
		t.Fatalf("named %v, want the camera's name", f)
	}
	ps, _ := batchPhotos(dir)
	if len(ps) < 2 || ps[0].File != "capt_003.jpg" {
		t.Fatalf("order %v", ps)
	}
}
