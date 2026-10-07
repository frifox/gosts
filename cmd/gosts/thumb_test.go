package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// TestThumbnail: a camera-sized JPEG shrinks to about thumbWidth wide,
// colours kept.
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
