package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestThumbnail: a camera-sized JPEG shrinks to about thumbWidth wide,
// colors kept.
func TestThumbnail(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 6000, 3376))
	for y := 0; y < 3376; y++ {
		for x := 0; x < 6000; x++ {
			c := color.RGBA{200, 120, 40, 255} // orange left
			if x >= 3000 {
				c = color.RGBA{40, 90, 200, 255} // blue right
			}
			src.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	th, err := thumbnail(buf.Bytes(), thumbWidth)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(th))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if b.Dx() < thumbWidth || b.Dx() > 2*thumbWidth || b.Dy() != b.Dx()*3376/6000 {
		t.Fatalf("thumbnail is %v", b)
	}
	near := func(a, b uint32) bool { d := int(a>>8) - int(b); return d > -12 && d < 12 }
	for _, c := range []struct {
		x       int
		r, g, b uint32
	}{{b.Dx() / 4, 200, 120, 40}, {b.Dx() * 3 / 4, 40, 90, 200}} {
		r, g, bl, _ := img.At(c.x, b.Dy()/2).RGBA()
		if !near(r, c.r) || !near(g, c.g) || !near(bl, c.b) {
			t.Errorf("at x %d: %d %d %d, want %d %d %d", c.x, r>>8, g>>8, bl>>8, c.r, c.g, c.b)
		}
	}
	t.Logf("%d KB → %d KB, %v", buf.Len()>>10, len(th)>>10, b)
}

// A camera's JPEG with its own preview after the photo (as a Sony's, 1616 ×
// 1080 after the 6000 × 4000): the preview's used for a thumbnail (the
// smallest at least as wide, the photo's shape), else the photo.
func TestJPEGPreview(t *testing.T) {
	enc := func(w, h int, c color.RGBA) []byte {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 255
		}
		var b bytes.Buffer
		jpeg.Encode(&b, img, &jpeg.Options{Quality: 85})
		return b.Bytes()
	}
	photo := enc(1500, 1000, color.RGBA{200, 40, 40, 255}) // red
	preview := enc(900, 600, color.RGBA{40, 200, 40, 255}) // green: the photo's shape
	square := enc(700, 700, color.RGBA{40, 40, 200, 255})  // blue: another shape
	small := enc(300, 200, color.RGBA{200, 200, 40, 255})  // too small
	file := bytes.Join([][]byte{photo, square, small, preview}, nil)

	if p := jpegPreview(file, 600); !bytes.Equal(p, preview) {
		t.Fatalf("the preview: got %d bytes, want the 900-wide (%d)", len(p), len(preview))
	}
	if p := jpegPreview(file, 1000); p != nil {
		t.Error("none wide enough: still found one")
	}
	if p := jpegPreview(photo, 600); p != nil {
		t.Error("a photo without one: found one")
	}
	th, err := thumbnail(file, 400)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(th))
	if err != nil {
		t.Fatal(err)
	}
	if w := img.Bounds().Dx(); w != 450 { // (900, halved)
		t.Errorf("thumbnail %d wide, want 450 (from the preview)", w)
	}
	if r, g, _, _ := img.At(200, 100).RGBA(); g < r {
		t.Error("thumbnail not from the (green) preview")
	}
}

// With GOSTS_THUMB_SAMPLE a real photo: how long its thumbnail takes now.
func TestThumbnailSample(t *testing.T) {
	path := os.Getenv("GOSTS_THUMB_SAMPLE")
	if path == "" {
		t.Skip("GOSTS_THUMB_SAMPLE not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	th, err := thumbnail(data, batchThumbWidth)
	if err != nil {
		t.Fatal(err)
	}
	img, _ := jpeg.Decode(bytes.NewReader(th))
	t.Logf("%s: thumbnail %v in %v (preview inside: %v)", filepath.Base(path), img.Bounds().Size(), time.Since(start), jpegPreview(data, batchThumbWidth) != nil)
}
