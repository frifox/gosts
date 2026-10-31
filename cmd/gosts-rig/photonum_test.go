package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A capture's photos show their shot's number (its file's: capt_001 is 1),
// not the session's count (seen: 213 on capt_001, photos taken and deleted
// before); a photo that isn't a shot's (Take Photo) shows the count.
func TestPhotoNumIsShots(t *testing.T) {
	simPictureWait = 5 * time.Second
	defer func() { simPictureWait = 2 * time.Second; newBatch() }()
	startBatch(false)
	var pend []photoEventMsg
	c := &cameraConn{out: func(m any) {
		if e, ok := m.(photoEventMsg); ok && e.Type == "photoPending" {
			pend = append(pend, e)
		}
	}}
	if err := c.connect(simCameraID); err != nil {
		t.Fatal(err)
	}
	c.taken = 212 // (the session's count, from before)
	c.numFor = func(n int) int {
		if n == 1 {
			return 1 // (the capture's first shot)
		}
		return 0
	}
	for i := 0; i < 2; i++ {
		if err := c.shoot(t.Context(), true, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(pend) != 2 || pend[0].Num != 1 || pend[1].Num != 214 {
		t.Fatalf("shown %+v, want 1 (the shot's), then 214 (the count: not a shot's)", pend)
	}
	c.simPicture(1, []byte("x"))
	if p, _ := c.photo(1); p.Num != 1 {
		t.Errorf("photo 1 shows %d, want 1", p.Num)
	}
}

// A capture opened again: its photos show their files' numbers (deleted
// ones leave gaps), not their place on the timeline.
func TestResumeInNums(t *testing.T) {
	dir := t.TempDir()
	defer newBatch()
	files := []string{"capt_001.jpg", "capt_002.jpg", "capt_005.jpg", "DSC0001.JPG"}
	for _, f := range files {
		os.WriteFile(filepath.Join(dir, f), grayJPEG(90), 0o644)
	}
	c := &cameraConn{out: func(any) {}}
	c.resumeIn(dir, files)
	for n, want := range map[int]int{1: 1, 2: 2, 3: 5, 4: 4} {
		if p, _ := c.photo(n); p.Num != want {
			t.Errorf("photo %d (%s) shows %d, want %d", n, files[n-1], p.Num, want)
		}
	}
}
