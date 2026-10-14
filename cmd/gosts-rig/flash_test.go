package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// flashCam is a camera whose flash fires `fires` times, then stops (dark
// photos), with one slow recycle (a dark photo) at shot `hiccup`.
type flashCam struct {
	mu            sync.Mutex
	n             int
	fires, hiccup int
	darks         []int // more photos that come out dark
}

func (f *flashCam) Shoot(ctx context.Context, s shutter) error {
	f.mu.Lock()
	f.n++
	n := f.n
	f.mu.Unlock()
	lum := uint8(150)
	if n > f.fires || n == f.hiccup || slices.Contains(f.darks, n) {
		lum = 15
	}
	img := image.NewGray(image.Rect(0, 0, 64, 48))
	for i := range img.Pix {
		img.Pix[i] = lum
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, nil)
	p := photo{JPEG: b.Bytes(), At: time.Now()}
	if s.folder != nil {
		path := filepath.Join(s.folder(), fmt.Sprintf("f%03d.jpg", n))
		os.WriteFile(path, p.JPEG, 0o644)
		p.Files = []string{path}
	}
	s.firing()
	go s.got(p, nil)
	return nil
}
func (f *flashCam) Close() error { return nil }

// Measure: shots till the flash stops (3 dark in a row; one alone is a slow
// recycle), the bright ones its limit, saved in the config.
func TestMeasureFlash(t *testing.T) {
	defer func(d time.Duration) { flashEvery = d }(flashEvery)
	flashEvery = 0
	cfg, _ := loadConfig(filepath.Join(t.TempDir(), "config.toml"))
	var mu sync.Mutex
	var last flashMsg
	cam := &cameraConn{cfg: cfg}
	a := &app{cfg: cfg, clients: map[*client]struct{}{}}
	out := func(m any) {
		if f, ok := m.(flashMsg); ok {
			mu.Lock()
			last = f
			mu.Unlock()
		}
	}
	cam.out = out
	a.camera = cam
	a.rig = &rig{cfg: cfg, out: out}
	a.cap = &capture{rig: a.rig, camera: cam, out: out}
	a.flash.out = out
	cam.cam = &flashCam{fires: 8, hiccup: 5} // 7 flashes (shot 5 didn't), then none
	if err := a.measureFlash("1/2"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); a.flash.running(); {
		if time.Now().After(deadline) {
			t.Fatal("still measuring")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if last.Error != "" || last.Result != 7 || !last.Saved || last.Shots != 11 {
		t.Fatalf("got %+v, want 7 flashes in 11 shots, saved", last)
	}
	if l := cfg.get().FlashLimits["1/2"]; l.Flashes != 7 || l.At.IsZero() {
		t.Fatalf("saved %+v", l)
	}
}

func TestMeanBrightness(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, color.RGBA{200, 200, 200, 255})
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 95})
	if v, err := meanBrightness(b.Bytes()); err != nil || v < 195 || v > 205 {
		t.Fatalf("%v %v", v, err)
	}
}
