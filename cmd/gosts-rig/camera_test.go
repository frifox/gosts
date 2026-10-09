package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The tests' simulated camera is never busy (simBusy) and its pictures come
// over at once (simDownload): they time things themselves; TestSimCameraBusy
// and TestSimDownload check those.
func init() { simBusy, simDownload = 0, 0 }

// The simulated camera is busy for simBusy after a photo: the next shutter
// waits, as on a real camera (so measuring its pace finds about simBusy).
func TestSimCameraBusy(t *testing.T) {
	defer func(b time.Duration) { simBusy = b }(simBusy)
	simBusy = 150 * time.Millisecond
	s := &simCamera{lag: 10 * time.Millisecond}
	var fired []time.Time
	for i := 0; i < 3; i++ {
		sh := shutter{firing: func() { fired = append(fired, time.Now()) }, got: func(photo, error) {}}
		if err := s.Shoot(t.Context(), sh); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i < len(fired); i++ {
		if gap := fired[i].Sub(fired[i-1]); gap < 140*time.Millisecond || gap > 300*time.Millisecond {
			t.Fatalf("shutters %v apart, want about %v", gap, simBusy)
		}
	}
}

// tiffWithJPEGs is a little-endian TIFF whose IFD0 points to small, and a
// SubIFD to big (as a RAW keeps its thumbnail and full preview).
func tiffWithJPEGs(small, big []byte) []byte {
	le := binary.LittleEndian
	var b bytes.Buffer
	b.Write([]byte("II"))
	binary.Write(&b, le, uint16(42))
	binary.Write(&b, le, uint32(8)) // IFD0 at 8
	entry := func(tag, typ uint16, count, val uint32) {
		binary.Write(&b, le, tag)
		binary.Write(&b, le, typ)
		binary.Write(&b, le, count)
		binary.Write(&b, le, val)
	}
	ifd1 := uint32(8 + 2 + 3*12 + 4) // after IFD0 (at 8)
	data := ifd1 + 2 + 2*12 + 4
	binary.Write(&b, le, uint16(3))
	entry(0x14a, 4, 1, ifd1)
	entry(0x201, 4, 1, data)
	entry(0x202, 4, 1, uint32(len(small)))
	binary.Write(&b, le, uint32(0))
	binary.Write(&b, le, uint16(2))
	entry(0x201, 4, 1, data+uint32(len(small)))
	entry(0x202, 4, 1, uint32(len(big)))
	binary.Write(&b, le, uint32(0))
	b.Write(small)
	b.Write(big)
	return b.Bytes()
}

func jpegOf(w, h int) []byte {
	var b bytes.Buffer
	jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)), nil)
	return b.Bytes()
}

func TestRawPreview(t *testing.T) {
	small, big := jpegOf(16, 12), jpegOf(320, 240)
	got, err := rawPreview(tiffWithJPEGs(small, big))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, big) {
		t.Fatalf("got %d bytes, want the larger preview (%d)", len(got), len(big))
	}
	if _, err := rawPreview([]byte("not a raw file at all")); err == nil {
		t.Fatal("no error for a non-TIFF file")
	}
}

func TestSaveUnique(t *testing.T) {
	dir := t.TempDir()
	a, _ := saveUnique(dir, "DSC01234.ARW", []byte("one"))
	b, _ := saveUnique(dir, "DSC01234.ARW", []byte("two"))
	if filepath.Base(a) != "DSC01234.ARW" || filepath.Base(b) != "DSC01234-2.ARW" {
		t.Fatalf("saved as %s, %s", a, b)
	}
	if d, _ := os.ReadFile(a); string(d) != "one" {
		t.Fatal("first file overwritten")
	}
}

func TestGphoto2Error(t *testing.T) {
	claim := "*** Error ***\nAn error occurred in the io-library ('Could not claim the USB device'): ...\n"
	if !strings.Contains(gphoto2Error([]byte(claim)), "killall ptpcamerad") {
		t.Fatal("no macOS hint for the claim error")
	}
	other := "*** Error ***\nERROR: Could not capture.\n\nFor debugging messages, please use the --debug option.\nmore"
	if got := gphoto2Error([]byte(other)); got != "ERROR: Could not capture." {
		t.Fatalf("got %q", got)
	}
}

// Simulated pictures come over one at a time, simDownload each, as over a
// camera's USB link: three rendered at once arrive one download apart.
func TestSimDownload(t *testing.T) {
	defer func(d time.Duration) { simDownload = d }(simDownload)
	simDownload = 100 * time.Millisecond
	simPictureWait = 5 * time.Second // the pages' renders, not made-up ones
	defer func() { simPictureWait = 2 * time.Second }()
	var mu sync.Mutex
	arrived := map[int]time.Time{}
	c := &cameraConn{out: func(m any) {
		if p, ok := m.(photoMsg); ok {
			mu.Lock()
			arrived[p.N] = time.Now()
			mu.Unlock()
		}
	}}
	if err := c.connect(simCameraID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := c.shoot(t.Context(), true, nil); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	for n := 1; n <= 3; n++ {
		if !c.simPicture(n, []byte("rendered")) {
			t.Fatalf("picture %d refused", n)
		}
	}
	if c.simPicture(2, []byte("again")) {
		t.Fatal("a second copy taken")
	}
	time.Sleep(450 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	for n := 1; n <= 3; n++ {
		at, ok := arrived[n]
		if !ok {
			t.Fatalf("picture %d didn't come", n)
		}
		want := time.Duration(n) * simDownload
		if d := at.Sub(start); d < want-30*time.Millisecond || d > want+80*time.Millisecond {
			t.Fatalf("picture %d came after %v, want about %v", n, d, want)
		}
	}
}
