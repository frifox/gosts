package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"sync"
	"time"
)

// Camera takes the photos. Drivers for real cameras (Sony A6600 over USB
// first) implement it; the simulated camera stands in for one so the rest
// can be tried without a camera.
type Camera interface {
	// Shoot takes one photo and returns it once taken (the rig may move on
	// then). JPEG may be empty if the picture comes later (or not at all).
	Shoot(ctx context.Context) (photo, error)
	Close() error
}

// photo is one photo taken.
type photo struct {
	JPEG []byte
	At   time.Time
}

// cameraInfo is a camera that can be connected, for the page's list.
type cameraInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// simCameraID selects the simulated camera.
const simCameraID = "sim"

// listCameras lists the cameras that can be connected.
func listCameras() []cameraInfo {
	return []cameraInfo{{ID: simCameraID, Name: "Simulator", Detail: "Simulated camera: takes each photo after a short shutter lag, saves nothing"}}
}

func openCamera(id string) (Camera, string, error) {
	switch id {
	case simCameraID:
		return &simCamera{lag: 30 * time.Millisecond}, "Simulator", nil
	}
	return nil, "", fmt.Errorf("no camera %q", id)
}

// simCamera "takes" a photo after a shutter lag. Its picture comes later:
// cameraConn asks a page to render the Rig View from the camera (simShoot),
// or makes one up if no page does.
type simCamera struct {
	lag time.Duration
	mu  sync.Mutex
	n   int
}

func (s *simCamera) Shoot(ctx context.Context) (photo, error) {
	select {
	case <-ctx.Done():
		return photo{}, ctx.Err()
	case <-time.After(s.lag):
	}
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
	return photo{At: time.Now()}, nil
}

// simPictureJPEG is simPicture n as a JPEG.
func simPictureJPEG(n int) []byte {
	var b bytes.Buffer
	jpeg.Encode(&b, simPicture(n), &jpeg.Options{Quality: 85})
	return b.Bytes()
}

// simPicture is the simulated camera's made-up photo n, when no page renders
// one: a backdrop, a turntable with an object on it, and n in the corner.
func simPicture(n int) image.Image {
	const w, h = 600, 400
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	hue := float64(n%12) / 12
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			f := float64(y) / h
			c := color.RGBA{uint8(40 + 30*f), uint8(44 + 30*f), uint8(52 + 34*f), 255}
			// The turntable: a dark ellipse.
			if dx, dy := float64(x-w/2)/170, float64(y-300)/34; dx*dx+dy*dy <= 1 {
				c = color.RGBA{28, 30, 34, 255}
			}
			// The object: a coloured dome on the turntable.
			if dx, dy := float64(x-w/2)/80, float64(y-300)/120; dx*dx+dy*dy <= 1 && y <= 300 {
				r, g, bl := hsv(hue, 0.55, 0.85*(0.75+0.25*(1-math.Abs(dx))))
				c = color.RGBA{r, g, bl, 255}
			}
			img.Set(x, y, c)
		}
	}
	drawNumber(img, n, 16, 16, 5)
	return img
}

func hsv(h, s, v float64) (uint8, uint8, uint8) {
	i := math.Floor(h * 6)
	f := h*6 - i
	p, q, t := v*(1-s), v*(1-f*s), v*(1-(1-f)*s)
	var r, g, b float64
	switch int(i) % 6 {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	default:
		r, g, b = v, p, q
	}
	return uint8(r * 255), uint8(g * 255), uint8(b * 255)
}

// digits are 3×5 pixel figures, a row of three bits per line.
var digits = [10][5]uint8{
	{7, 5, 5, 5, 7}, {2, 6, 2, 2, 7}, {7, 1, 7, 4, 7}, {7, 1, 7, 1, 7}, {5, 5, 7, 1, 1},
	{7, 4, 7, 1, 7}, {7, 4, 7, 5, 7}, {7, 1, 1, 1, 1}, {7, 5, 7, 5, 7}, {7, 5, 7, 1, 7},
}

// drawNumber writes n at x, y in white, each figure pixel scale×scale.
func drawNumber(img *image.RGBA, n, x, y, scale int) {
	for _, ch := range fmt.Sprint(n) {
		d := digits[ch-'0']
		for row := 0; row < 5; row++ {
			for col := 0; col < 3; col++ {
				if d[row]>>(2-col)&1 == 0 {
					continue
				}
				for i := 0; i < scale; i++ {
					for j := 0; j < scale; j++ {
						img.Set(x+col*scale+i, y+row*scale+j, color.White)
					}
				}
			}
		}
		x += 4 * scale
	}
}

func (s *simCamera) Close() error { return nil }

// cameraConn is the connected camera, if any, and the last photo taken.
type cameraConn struct {
	out func(any)

	mu      sync.Mutex
	cam     Camera
	id      string
	name    string
	last    photo
	count   int // photos taken, numbering them
	shown   int // the photo last is
	pending int // simulated photo waiting for its picture from a page
}

// simShootMsg asks the pages to render the simulated camera's photo n (the
// Rig View from the camera) and post it to /photo/sim?n=.
type simShootMsg struct {
	Type string `json:"type"` // "simShoot"
	N    int    `json:"n"`
}

// simPictureWait is how long a simulated photo waits for a page's render
// before a made-up picture stands in.
var simPictureWait = 2 * time.Second

// photoMsg tells the page a photo was taken (it fetches /photo/last.jpg).
type photoMsg struct {
	Type string `json:"type"` // "photo"
	N    int    `json:"n"`
	At   int64  `json:"at"` // ms since 1970
}

// cameraMsg tells the page which camera is connected.
type cameraMsg struct {
	Type      string `json:"type"` // "camera"
	Connected bool   `json:"connected"`
	ID        string `json:"id"`
	Name      string `json:"name"`
}

func (c *cameraConn) msg() cameraMsg {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cameraMsg{Type: "camera", Connected: c.cam != nil, ID: c.id, Name: c.name}
}

func (c *cameraConn) connect(id string) error {
	cam, name, err := openCamera(id)
	if err != nil {
		return err
	}
	c.mu.Lock()
	old := c.cam
	c.cam, c.id, c.name = cam, id, name
	c.mu.Unlock()
	if old != nil {
		old.Close()
	}
	c.out(c.msg())
	return nil
}

func (c *cameraConn) disconnect() {
	c.mu.Lock()
	old := c.cam
	c.cam, c.id, c.name = nil, "", ""
	c.mu.Unlock()
	if old != nil {
		old.Close()
	}
	c.out(c.msg())
}

func (c *cameraConn) connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cam != nil
}

var errNoCamera = errors.New("no camera connected")

// shoot takes a photo with the connected camera; it becomes the last photo.
func (c *cameraConn) shoot(ctx context.Context) error {
	c.mu.Lock()
	cam := c.cam
	c.mu.Unlock()
	if cam == nil {
		return errNoCamera
	}
	p, err := cam.Shoot(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.count++
	n := c.count
	_, sim := cam.(*simCamera)
	if sim {
		c.pending = n
	}
	c.mu.Unlock()
	switch {
	case len(p.JPEG) > 0:
		c.picture(n, p)
	case sim: // a page renders it; if none does, a made-up one
		c.out(simShootMsg{Type: "simShoot", N: n})
		time.AfterFunc(simPictureWait, func() { c.picture(n, photo{JPEG: simPictureJPEG(n), At: p.At}) })
	}
	return nil
}

// picture sets photo n's picture, making it the last photo (once: a later
// copy of the same photo is ignored, and so is an older photo's).
func (c *cameraConn) picture(n int, p photo) bool {
	c.mu.Lock()
	if n <= c.shown {
		c.mu.Unlock()
		return false
	}
	c.last, c.shown = p, n
	if c.pending == n {
		c.pending = 0
	}
	c.mu.Unlock()
	c.out(photoMsg{Type: "photo", N: n, At: p.At.UnixMilli()})
	return true
}

// simPicture sets simulated photo n's picture, rendered by a page.
func (c *cameraConn) simPicture(n int, jpg []byte) bool {
	return c.picture(n, photo{JPEG: jpg, At: time.Now()})
}

// lastPhoto is the last photo with a picture (empty if none), and its number.
func (c *cameraConn) lastPhoto() (photo, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last, c.shown
}
