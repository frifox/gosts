package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
)

// thumbWidth is how wide (at least) a photo's thumbnail is: the timeline's
// and All Photos' tiles (up to ~440 px), sharp enough on a 2× screen.
const thumbWidth = 800

// thumbnail shrinks a JPEG to about width pixels wide (a whole fraction of
// its own, averaging each block). The page shows these on the timeline:
// decoding the camera's own (24 MP) for a tile held up the Rig View's
// animation for a frame or five with every photo.
func thumbnail(jpg []byte, width int) ([]byte, error) {
	src, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	k := max(1, b.Dx()/width) // a k×k block per pixel
	w, h := b.Dx()/k, b.Dy()/k
	var dst image.Image
	switch s := src.(type) {
	case *image.YCbCr: // a camera's: average Y, Cb, Cr straight off the planes
		d := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio444)
		n := k * k
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var sy, scb, scr int
				for j := 0; j < k; j++ {
					py := b.Min.Y + y*k + j
					for i := 0; i < k; i++ {
						px := b.Min.X + x*k + i
						sy += int(s.Y[s.YOffset(px, py)])
						c := s.COffset(px, py)
						scb += int(s.Cb[c])
						scr += int(s.Cr[c])
					}
				}
				o := d.YOffset(x, y)
				d.Y[o], d.Cb[o], d.Cr[o] = uint8(sy/n), uint8(scb/n), uint8(scr/n)
			}
		}
		dst = d
	default: // anything else (gray, CMYK…): slower, but rare
		d := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var sr, sg, sb uint32
				for j := 0; j < k; j++ {
					for i := 0; i < k; i++ {
						r, g, bl, _ := src.At(b.Min.X+x*k+i, b.Min.Y+y*k+j).RGBA()
						sr, sg, sb = sr+r, sg+g, sb+bl
					}
				}
				n := uint32(k * k)
				d.SetRGBA(x, y, color.RGBA{uint8(sr / n >> 8), uint8(sg / n >> 8), uint8(sb / n >> 8), 255})
			}
		}
		dst = d
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// thumb is photo n's thumbnail, made the first time it's asked for.
func (c *cameraConn) thumb(n int) ([]byte, bool) {
	p, ok := c.photo(n)
	if !ok {
		return nil, false
	}
	if p.Thumb != nil {
		return p.Thumb, true
	}
	t, err := thumbnail(p.JPEG, thumbWidth)
	if err != nil {
		return p.JPEG, true // can't shrink it: the photo itself
	}
	c.mu.Lock()
	if q, ok := c.photos[n]; ok && q.At.Equal(p.At) { // still that picture
		q.Thumb = t
		c.photos[n] = q
	}
	c.mu.Unlock()
	return t, true
}
