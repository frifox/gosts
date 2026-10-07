package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Camera takes the photos. Drivers for real cameras (Sony A6600 over USB
// first) implement it; the simulated camera stands in for one so the rest
// can be tried without a camera.
type Camera interface {
	// Shoot takes one photo: firing is called just as the shutter is
	// released (where the rig is then is where the photo was taken), it
	// returns once the shutter has fired (the rig may move on then), and got
	// is called with its picture once that has come over, maybe later and
	// from another goroutine (JPEG empty: nothing to show), or with an error.
	Shoot(ctx context.Context, firing func(), got func(photo, error)) error
	Close() error
}

// pacedCamera is a camera that needs time between photos: Moving Shots
// spaces the shots at least MinInterval apart.
type pacedCamera interface {
	MinInterval() time.Duration
}

// photo is one photo taken.
type photo struct {
	JPEG  []byte   // to show; the camera's, or a RAW file's preview
	Files []string // where the camera's own files were saved, if anywhere
	At    time.Time
}

// photoDir is where real cameras' photos are saved (a folder a day); main
// sets it from the config.
var photoDir = defaultPhotoDir()

func defaultPhotoDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "gosts-photos"
	}
	return filepath.Join(home, "Pictures", "gosts")
}

// cameraInfo is a camera that can be connected, for the page's list.
type cameraInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// simCameraID selects the simulated camera.
const simCameraID = "sim"

// listCameras lists the cameras that can be connected: the simulator, and
// whatever gphoto2 finds attached over USB right now (the Sony A6600,
// PTP-connected).
func listCameras() []cameraInfo {
	cams := []cameraInfo{{ID: simCameraID, Name: "Simulator", Detail: "Simulated camera: takes each photo after a short shutter lag, saves nothing"}}
	for _, d := range gphoto2Detect() {
		cams = append(cams, cameraInfo{ID: gphoto2Prefix + d.port, Name: d.model, Detail: "USB, " + d.port + " (gphoto2)"})
	}
	return cams
}

func openCamera(id string) (Camera, string, error) {
	switch {
	case id == simCameraID:
		return &simCamera{lag: 30 * time.Millisecond}, "Simulator", nil
	case strings.HasPrefix(id, gphoto2Prefix):
		return openGphoto2Camera(strings.TrimPrefix(id, gphoto2Prefix))
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

func (s *simCamera) Shoot(ctx context.Context, firing func(), got func(photo, error)) error {
	firing()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.lag):
	}
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
	got(photo{At: time.Now()}, nil) // no picture: cameraConn has one rendered
	return nil
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

// cameraConn is the connected camera, if any, and the photos taken (the
// page's timeline, until Reset).
type cameraConn struct {
	out func(any)

	mu      sync.Mutex
	cam     Camera
	id      string
	name    string
	photos  map[int]photo   // by number, those with a picture
	count   int             // photos taken, numbering them (on across Resets)
	pending int             // simulated photo waiting for its picture from a page
	queue   chan queuedShot // photos to take without waiting (see shoot)
	deleted map[int]bool    // photos deleted (a picture still coming is dropped)
}

// maxPhotos is how many photos the timeline keeps in memory; the oldest go.
const maxPhotos = 1000

// photoInfo is a photo on the timeline, for the page (the picture is at
// /photo/{n}.jpg).
type photoInfo struct {
	N  int   `json:"n"`
	At int64 `json:"at"` // ms since 1970
}

// photosMsg is the whole timeline, oldest first (on connecting, and after
// Reset).
type photosMsg struct {
	Type   string      `json:"type"` // "photos"
	Photos []photoInfo `json:"photos"`
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

// photoMsg tells the page a photo was taken (it fetches /photo/{n}.jpg).
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

// shoot takes a photo with the connected camera; its picture joins the
// timeline when it comes.
//
// With wait it returns once the shutter has fired (the rig must keep still
// till then). Without, it returns at once and the photo is taken in turn by
// a worker (Moving Shots: the rig keeps moving); a failure then only shows
// on the page and in the log.
// firing is called just as the shutter is released (from another goroutine
// when not waiting).
func (c *cameraConn) shoot(ctx context.Context, wait bool, firing func()) error {
	if firing == nil {
		firing = func() {}
	}
	c.mu.Lock()
	cam := c.cam
	if cam == nil {
		c.mu.Unlock()
		return errNoCamera
	}
	// The photo's number now, so the page can show a placeholder while the
	// camera takes it and its picture comes over.
	c.count++
	n := c.count
	c.mu.Unlock()
	c.out(photoEventMsg{Type: "photoPending", N: n})
	got := func(p photo, err error) { c.got(cam, n, p, err) }
	if wait {
		if err := cam.Shoot(ctx, firing, got); err != nil {
			c.out(photoEventMsg{Type: "photoFailed", N: n})
			return err
		}
		return nil
	}
	c.mu.Lock()
	if c.queue == nil {
		c.queue = make(chan queuedShot, 256)
		go c.shooter(c.queue)
	}
	q := c.queue
	c.mu.Unlock()
	q <- queuedShot{ctx: ctx, cam: cam, n: n, firing: firing, got: got, asked: time.Now()}
	return nil
}

// queuedShot is a photo for the shooter to take.
type queuedShot struct {
	ctx    context.Context
	cam    Camera
	n      int
	firing func()
	got    func(photo, error)
	asked  time.Time
}

// shooter takes queued photos in turn.
func (c *cameraConn) shooter(q chan queuedShot) {
	for s := range q {
		if err := s.cam.Shoot(s.ctx, s.firing, s.got); err != nil {
			c.out(photoEventMsg{Type: "photoFailed", N: s.n})
			if s.ctx.Err() == nil {
				c.warn("photo %d: %v", s.n, err)
			}
			continue
		}
		// The rig moved on meanwhile: how late the shutter was says how far
		// from its point the photo was taken.
		log.Printf("camera: photo %d fired %v after it was asked for", s.n, time.Since(s.asked).Round(time.Millisecond))
	}
}

// minInterval is how soon after one photo the connected camera can take the
// next (0: at once).
func (c *cameraConn) minInterval() time.Duration {
	c.mu.Lock()
	cam := c.cam
	c.mu.Unlock()
	if p, ok := cam.(pacedCamera); ok {
		return p.MinInterval()
	}
	return 0
}

// got is photo n's picture (or failure), from the camera.
func (c *cameraConn) got(cam Camera, n int, p photo, err error) {
	_, sim := cam.(*simCamera)
	switch {
	case err != nil:
		c.out(photoEventMsg{Type: "photoFailed", N: n})
		c.warn("photo %d: %v", n, err)
	case len(p.JPEG) > 0:
		c.picture(n, p)
	case sim: // a page renders it; if none does, a made-up one
		c.mu.Lock()
		c.pending = n
		c.mu.Unlock()
		c.out(simShootMsg{Type: "simShoot", N: n})
		time.AfterFunc(simPictureWait, func() { c.picture(n, photo{JPEG: simPictureJPEG(n), At: p.At}) })
	default: // taken, but nothing to show
		c.out(photoEventMsg{Type: "photoFailed", N: n})
	}
}

// warn logs a camera problem, also on the page.
func (c *cameraConn) warn(format string, args ...any) {
	msg := "camera: " + fmt.Sprintf(format, args...)
	log.Print(msg)
	c.out(logMsg{Type: "log", Level: "error", Message: msg})
}

// photoEventMsg tells the page about photo n: being taken ("photoPending";
// its picture follows as a photoMsg), taken without a picture or failed
// ("photoFailed"), or deleted ("photoDeleted").
type photoEventMsg struct {
	Type string `json:"type"`
	N    int    `json:"n"`
}

// delete takes photo n off the timeline (its files, if saved, stay where
// they are). A photo still coming is dropped when it arrives.
func (c *cameraConn) delete(n int) {
	c.mu.Lock()
	delete(c.photos, n)
	if c.deleted == nil {
		c.deleted = map[int]bool{}
	}
	c.deleted[n] = true
	c.mu.Unlock()
	c.out(photoEventMsg{Type: "photoDeleted", N: n})
}

// picture sets photo n's picture, adding it to the timeline (once: a later
// copy of the same photo is ignored).
func (c *cameraConn) picture(n int, p photo) bool {
	c.mu.Lock()
	if _, ok := c.photos[n]; ok || c.deleted[n] || n > c.count || n <= c.count-maxPhotos {
		c.mu.Unlock()
		return false
	}
	if c.photos == nil {
		c.photos = map[int]photo{}
	}
	c.photos[n] = p
	for k := range c.photos {
		if k <= c.count-maxPhotos {
			delete(c.photos, k)
		}
	}
	if c.pending == n {
		c.pending = 0
	}
	c.mu.Unlock()
	c.out(photoMsg{Type: "photo", N: n, At: p.At.UnixMilli()})
	return true
}

// photo is photo n's picture, if it's on the timeline.
func (c *cameraConn) photo(n int) (photo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.photos[n]
	return p, ok
}

// timeline lists the photos, oldest first.
func (c *cameraConn) timeline() photosMsg {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := photosMsg{Type: "photos", Photos: []photoInfo{}}
	for n, p := range c.photos {
		m.Photos = append(m.Photos, photoInfo{N: n, At: p.At.UnixMilli()})
	}
	slices.SortFunc(m.Photos, func(a, b photoInfo) int { return a.N - b.N })
	return m
}

// reset clears the timeline (Reset).
func (c *cameraConn) reset() {
	c.mu.Lock()
	c.photos = nil
	c.mu.Unlock()
	c.out(c.timeline())
}

// simPicture sets simulated photo n's picture, rendered by a page.
func (c *cameraConn) simPicture(n int, jpg []byte) bool {
	return c.picture(n, photo{JPEG: jpg, At: time.Now()})
}

// lastPhoto is the newest photo on the timeline (empty if none), and its
// number.
func (c *cameraConn) lastPhoto() (photo, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	last := 0
	for n := range c.photos {
		last = max(last, n)
	}
	return c.photos[last], last
}
