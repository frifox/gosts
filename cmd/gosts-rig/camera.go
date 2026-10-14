package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io/fs"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Camera takes the photos. Drivers for real cameras (Sony A6600 over USB
// first) implement it; the simulated camera stands in for one so the rest
// can be tried without a camera.
type Camera interface {
	// Shoot takes one photo (see shutter), returning once the shutter has
	// fired (the rig may move on then).
	Shoot(ctx context.Context, s shutter) error
	Close() error
}

// shutter is what Shoot calls back for a photo.
type shutter struct {
	// firing is called just as the shutter is released: where the rig is
	// then is where the photo was taken.
	firing func()
	// folder is where the photo's files are saved (asked as it's taken).
	folder func() string
	// got is called with its picture once that has come over, maybe later
	// and from another goroutine (JPEG empty: nothing to show), or with an
	// error.
	got func(photo, error)
}

// pacedCamera is a camera that needs time between photos: Moving Shots
// spaces the shots at least MinInterval apart.
type pacedCamera interface {
	MinInterval() time.Duration
}

// photo is one photo taken.
type photo struct {
	JPEG     []byte   // to show; the camera's, or a RAW file's preview
	Thumb    []byte   // it shrunk, for the timeline (see thumb); made when first wanted
	Files    []string // where the camera's own files were saved, if anywhere
	At       time.Time
	Sample   bool              // a sample shot (Config): not on the timeline
	Num      int               // its number on the timeline (1, 2…: sample shots not counted); 0 for a sample
	Caption  string            // its settings, for a sample
	Settings map[string]string // and them by key (e.g. for balancing from a gray card)
	dir      string            // the simulated camera's: the folder to save it in (as the shutter went)
}

// fileName is the photo's file's name (its first: the JPEG), "" if unsaved.
func (p photo) fileName() string {
	if len(p.Files) == 0 {
		return ""
	}
	return filepath.Base(p.Files[0])
}

// photoDir is where real cameras' photos are saved (a folder per batch, see
// batchFolder); main sets it from the config.
var photoDir = defaultPhotoDir()

// The batch: the photos since the app started, or since Reset. Each batch's
// files go in a folder of their own, named after when its first photo was
// taken ("2006-01-02 15.04.05": colons aren't for file names on a Mac), made
// only then; its sample shots (Config) in one beside it, the same name with
// "-samples".
var batch struct {
	sync.Mutex
	dir string
}

const batchName = "2006-01-02 15.04.05"

// batchFolder is the current batch's folder (made now if it's the batch's
// first photo).
func batchFolder() string { return batchDir("") }

// sampleFolder is the current batch's sample shots' folder: "samples" in
// its folder (made now if it's the first), so they go with the capture.
func sampleFolder() string { return batchDir(string(filepath.Separator) + sampleSub) }

// sampleSub is the sample shots' folder in a capture's.
const sampleSub = "samples"

func batchDir(suffix string) string {
	batch.Lock()
	defer batch.Unlock()
	if batch.dir == "" { // the batch's name: when it first takes something
		batch.dir = freeBatchDir()
	}
	dir := batch.dir + suffix
	if _, err := os.Stat(dir); err != nil {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("photos: %v", err)
		} else {
			log.Printf("photos: new folder %s", dir)
		}
	}
	return dir
}

// newBatch starts a new batch: the next photo makes its folder.
func newBatch() {
	batch.Lock()
	batch.dir = ""
	batch.Unlock()
}

// startBatch starts a new batch, named now (its start time); its folder made
// now too with make (New Capture), else with its first photo or sample
// shot (on starting up: no empty folder each time).
func startBatch(make bool) {
	batch.Lock()
	batch.dir = freeBatchDir()
	dir := batch.dir
	batch.Unlock()
	if make {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("photos: %v", err)
		} else {
			log.Printf("photos: new folder %s", dir)
		}
	}
}

// freeBatchDir is a new batch's folder: named after now, "… 2" and on if
// that's taken (two in a second).
func freeBatchDir() string {
	base := filepath.Join(photoDir, time.Now().Format(batchName))
	dir := base
	for i := 2; ; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return dir
		}
		dir = fmt.Sprintf("%s %d", base, i)
	}
}

// batchName is the current batch's name (its folder's), "" if none yet.
func currentBatch() string {
	batch.Lock()
	defer batch.Unlock()
	if batch.dir == "" {
		return ""
	}
	return filepath.Base(batch.dir)
}

// batchMsg tells the pages the current capture's name (its folder), and
// whether it's empty: no photos or sample shots (taken, coming, or in its
// folder): New Capture then has nothing to start afresh from.
type batchMsg struct {
	Type  string `json:"type"` // "batch"
	Name  string `json:"name"`
	Empty bool   `json:"empty"`
	Dir   string `json:"dir"` // the folder the captures are in (~ for home)
}

// batchStatus is the current capture's batchMsg: empty if the timeline
// (samples too) has nothing taken or coming and its folder no files.
func (c *cameraConn) batchStatus() batchMsg {
	c.mu.Lock()
	n := len(c.photos) + len(c.inFlight)
	c.mu.Unlock()
	m := batchNow()
	m.Empty = n == 0 && (m.Name == "" || !hasFiles(filepath.Join(photoDir, m.Name)))
	return m
}

// hasFiles is whether dir (or a folder in it) has a file (not hidden).
func hasFiles(dir string) bool {
	found := false
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func batchNow() batchMsg {
	return batchMsg{Type: "batch", Name: currentBatch(), Dir: homeShort(photoDir)}
}

func defaultPhotoDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "gosts-rig-photos"
	}
	return filepath.Join(home, "Pictures", "gosts-rig")
}

// cameraInfo is a camera that can be connected, for the page's list.
type cameraInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// previewCamera is a camera with a live view: Preview is a frame of it now
// (a JPEG, smaller than a photo; nothing is saved). Camera Settings' live
// preview asks for one after another.
type previewCamera interface {
	Preview() ([]byte, error)
}

// simCameraID selects the simulated camera.
const simCameraID = "sim"

// listCameras lists the cameras that can be connected: the simulator, and
// whatever gphoto2 finds attached over USB right now (the Sony A6600,
// PTP-connected).
func listCameras() []cameraInfo {
	cams := []cameraInfo{{ID: simCameraID, Name: "Simulator", Detail: "Simulated camera: takes a photo about every second, as a real one would, saved as a JPEG of the Rig View"}}
	for _, d := range gphoto2Detect() {
		cams = append(cams, cameraInfo{ID: gphoto2Prefix + d.port, Name: d.model, Detail: "USB, " + d.port + " (gphoto2)"})
	}
	return cams
}

// lastCameras is the list last made by scanning (see camerasNow).
var lastCameras struct {
	sync.Mutex
	list []cameraInfo
}

// camerasNow lists the cameras for the page: scanning the USB bus, unless
// a camera's being opened or is open here, or another program has one open
// (a gphoto2 shell: another gosts-rig, say): then the list made last. A
// scan then can upset the camera mid-handshake (the A6600 stuck on
// "Connecting…").
func (c *cameraConn) camerasNow() []cameraInfo {
	c.mu.Lock()
	busy := c.opening > 0 || c.cam != nil
	c.mu.Unlock()
	lastCameras.Lock()
	defer lastCameras.Unlock()
	if (busy || gphoto2Elsewhere()) && lastCameras.list != nil {
		return lastCameras.list
	}
	lastCameras.list = listCameras()
	return lastCameras.list
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

// simBusy is how long the simulated camera is busy after a photo (saving
// it), as a real one: the next shutter waits till then. An A6600 shooting
// RAW + JPEG keeps up with about one every 1.2 s, JPEG only a little faster.
var simBusy = time.Second

// simCamera "takes" a photo after a shutter lag. Its picture comes later:
// cameraConn asks a page to render the Rig View from the camera (simShoot),
// or makes one up if no page does.
type simCamera struct {
	lag      time.Duration
	shootMu  sync.Mutex // one photo at a time
	readyAt  time.Time  // busy with the last photo till then (see simBusy)
	mu       sync.Mutex
	n        int
	settings map[string]string // see Settings
	focus    int               // the lens's focus: sharp at 0 (see Autofocus)
	focusSet bool              // focus set (else it's simFocusStart)
}

func (s *simCamera) Shoot(ctx context.Context, sh shutter) error {
	s.shootMu.Lock()
	defer s.shootMu.Unlock()
	select { // still saving the last one
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Until(s.readyAt)):
	}
	sh.firing()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.lag):
	}
	s.readyAt = time.Now().Add(simBusy - s.lag)
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
	p := photo{At: time.Now()}
	if sh.folder != nil {
		p.dir = sh.folder()
	}
	sh.got(p, nil) // no picture: cameraConn has one rendered (and saves it there)
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
			// The object: a colored dome on the turntable.
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

// Battery: the simulated camera's never runs down.
func (s *simCamera) Battery() (string, error) { return "100%", nil }

// cameraConn is the connected camera, if any, and the photos taken (the
// page's timeline, until Reset).
type cameraConn struct {
	out func(any)
	cfg *configFile // for the cameras' measured pace (CameraPace)

	mu             sync.Mutex
	cam            Camera
	id             string
	name           string
	photos         map[int]photo             // by number, those with a picture
	count          int                       // photos taken, numbering them (on across Resets)
	pending        int                       // simulated photo waiting for its picture from a page
	queue          chan queuedShot           // photos to take without waiting (see shoot)
	deleted        map[int]bool              // photos deleted (a picture still coming is dropped)
	samples        map[int]string            // sample shots (off the timeline): their captions
	sampleSettings map[int]map[string]string // and settings
	gen            int                       // Resets so far (see generation)
	battery        string                    // its battery level, last read (see pollBattery)
	focal          float64                   // the focal length (mm) its last photo was taken at (EXIF), 0 if not known
	inFlight       map[int]bool              // photos asked for whose picture hasn't come yet (nor failed): see backlog
	unfired        []time.Time               // photos queued whose shutter hasn't gone yet: when each was asked for (see firingLate)
	firedAt        []time.Time               // when the queued photos' shutters went, recently (see measurePace)
	simRendered    map[int]bool              // simulated photos a page has rendered (coming over: see simPicture)
	simDirs        map[int]string            // the folders simulated photos are saved in (see simSave)
	onPicture      func(n int, p photo)      // a photo's picture came over (the capture records its file: see capture.pictured)
	nameFor        func(n int) string        // the name (no extension) photo n's files get, "" for the camera's (see capture.fileStem)
	simLink        time.Time                 // the simulated camera's link is busy till then (see simDownload)
	exposed        time.Duration             // the shutter speed last read (see exposure), for the estimate
	taken          int                       // the capture's photos asked for, sample shots not counted: their numbers to show (see photoInfo)
	nums           map[int]int               // and photo n's
	opening        int                       // cameras being opened (see camerasNow)
}

// maxPhotos is how many photos the timeline keeps in memory; the oldest go.
const maxPhotos = 1000

// photoInfo is a photo on the timeline, for the page (the picture is at
// /photo/{n}.jpg).
type photoInfo struct {
	N    int    `json:"n"`
	Num  int    `json:"num"`            // its number to show: the capture's photos counted, not sample shots
	File string `json:"file,omitempty"` // its file's name (the first: its JPEG), if saved
	At   int64  `json:"at"`             // ms since 1970
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
	Type     string  `json:"type"` // "simShoot"
	N        int     `json:"n"`
	Exposure float64 `json:"exposure"` // brighter (>1) or darker than normal, from its settings
	// Its white balance (and color temperature): the picture is tinted as a
	// camera set so would tint the simulated studio light.
	Settings map[string]string `json:"settings,omitempty"`
	Blur     float64           `json:"blur,omitempty"` // out of focus by (px, see simCamera.blur)
}

// simPictureWait is how long a simulated photo waits for a page's render
// before a made-up picture stands in.
var simPictureWait = 2 * time.Second

// simDownload is how long a simulated photo's picture takes to come over,
// one at a time, as over a real camera's USB link (an A6600's JPEG: about
// 0.7 s). The page renders it as the shutter goes; it arrives this long after.
var simDownload = 700 * time.Millisecond

// photoMsg tells the page a photo was taken (it fetches /photo/{n}.jpg).
type photoMsg struct {
	Type     string            `json:"type"` // "photo"
	N        int               `json:"n"`
	Num      int               `json:"num,omitempty"`  // (see photoInfo)
	File     string            `json:"file,omitempty"` // (see photoInfo)
	At       int64             `json:"at"`             // ms since 1970
	Sample   bool              `json:"sample,omitempty"`
	Caption  string            `json:"caption,omitempty"`
	Settings map[string]string `json:"settings,omitempty"` // a sample's settings, by key
}

// cameraMsg tells the page which camera is connected.
type cameraMsg struct {
	Type      string  `json:"type"` // "camera"
	Connected bool    `json:"connected"`
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Battery   string  `json:"battery,omitempty"` // e.g. "92%", if the camera says
	Pace      float64 `json:"pace,omitempty"`    // how often (s) it keeps up taking photos, as measured (0: not yet)
}

// batteryCamera is a camera that tells its battery level.
type batteryCamera interface {
	Battery() (string, error)
}

// batteryEvery is how often the battery level is read.
const batteryEvery = time.Minute

// pollBattery reads the connected camera's battery level now and then (and
// when one connects), telling the pages when it changes.
func (c *cameraConn) pollBattery(ctx context.Context) {
	t := time.NewTicker(batteryEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.readBattery()
		}
	}
}

func (c *cameraConn) readBattery() {
	c.mu.Lock()
	b, ok := c.cam.(batteryCamera)
	c.mu.Unlock()
	if !ok {
		return
	}
	level, err := b.Battery()
	if err != nil {
		return
	}
	c.mu.Lock()
	changed := level != c.battery
	c.battery = level
	c.mu.Unlock()
	if changed {
		c.out(c.msg())
	}
}

func (c *cameraConn) msg() cameraMsg {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cameraMsg{Type: "camera", Connected: c.cam != nil, ID: c.id, Name: c.name, Battery: c.battery, Pace: c.paceOf(c.id)}
}

func (c *cameraConn) connect(id string) error {
	c.mu.Lock()
	c.opening++ // (no scanning meanwhile: see camerasNow)
	c.mu.Unlock()
	cam, name, err := openCamera(id)
	c.mu.Lock()
	c.opening--
	c.mu.Unlock()
	if err != nil {
		return err
	}
	c.mu.Lock()
	old := c.cam
	c.cam, c.id, c.name, c.battery, c.focal = cam, id, name, "", 0
	c.mu.Unlock()
	if old != nil {
		old.Close()
	}
	c.out(c.msg())
	go c.readBattery() // now, not in a minute
	return nil
}

func (c *cameraConn) disconnect() {
	c.mu.Lock()
	old := c.cam
	c.cam, c.id, c.name, c.battery, c.focal = nil, "", "", "", 0
	c.mu.Unlock()
	if old != nil {
		old.Close()
	}
	c.out(c.msg())
}

// simulated: the simulated camera is connected.
func (c *cameraConn) simulated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, sim := c.cam.(*simCamera)
	return sim
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
	return c.take(ctx, wait, firing, false)
}

// sample takes a sample shot (the Config dialog): like Take Photo, but kept
// off the timeline, and captioned with the settings it was taken at.
func (c *cameraConn) sample(ctx context.Context) error {
	return c.take(ctx, true, nil, true)
}

func (c *cameraConn) take(ctx context.Context, wait bool, firing func(), sample bool) error {
	_, err := c.takeIn(ctx, wait, firing, sample, nil, nil)
	return err
}

// takeIn is take, its files saved in folder (nil: the capture's, or its
// samples'), and the photo's number (0 if it wasn't taken); numbered, if
// set, is told the number as soon as it's given (before the shutter: its
// picture may come over before takeIn returns).
func (c *cameraConn) takeIn(ctx context.Context, wait bool, firing func(), sample bool, folder func() string, numbered func(n int)) (int, error) {
	if firing == nil {
		firing = func() {}
	}
	c.mu.Lock()
	cam := c.cam
	c.mu.Unlock()
	if cam == nil {
		return 0, errNoCamera
	}
	caption := ""
	var settings map[string]string
	if sc, ok := cam.(settingsCamera); ok && sample {
		if ss, err := sc.Settings(); err == nil {
			caption = settingsCaption(ss)
			settings = map[string]string{}
			for _, st := range ss {
				settings[st.Key] = st.Current
			}
		}
	}
	// The photo's number now, so the page can show a placeholder while the
	// camera takes it and its picture comes over.
	c.mu.Lock()
	c.count++
	n, gen := c.count, c.gen
	if numbered != nil {
		c.mu.Unlock()
		numbered(n)
		c.mu.Lock()
	}
	num := 0
	if !sample { // (a sample shot isn't counted: the photos are 1, 2… all the same)
		c.taken++
		num = c.taken
		if c.nums == nil {
			c.nums = map[int]int{}
		}
		c.nums[n] = num
	}
	if c.inFlight == nil {
		c.inFlight = map[int]bool{}
	}
	c.inFlight[n] = true
	if sample {
		if c.samples == nil {
			c.samples = map[int]string{}
		}
		c.samples[n] = caption
		if c.sampleSettings == nil {
			c.sampleSettings = map[int]map[string]string{}
		}
		c.sampleSettings[n] = settings
	}
	c.mu.Unlock()
	c.out(photoEventMsg{Type: "photoPending", N: n, Num: num, Sample: sample})
	c.out(c.batchStatus())
	sh := shutter{firing: firing, folder: batchFolder, got: func(p photo, err error) {
		if c.current(gen) { // not a photo from before a Reset
			c.got(cam, n, p, err)
		}
	}}
	if sample {
		sh.folder = sampleFolder
	}
	if folder != nil {
		sh.folder = folder
	}
	if wait {
		if err := cam.Shoot(ctx, sh); err != nil {
			c.landed(n)
			c.out(photoEventMsg{Type: "photoFailed", N: n, Sample: sample})
			c.out(c.batchStatus())
			return 0, err
		}
		return n, nil
	}
	c.mu.Lock()
	if c.queue == nil {
		c.queue = make(chan queuedShot, 256)
		go c.shooter(c.queue)
	}
	q := c.queue
	c.unfired = append(c.unfired, time.Now())
	c.mu.Unlock()
	q <- queuedShot{ctx: ctx, cam: cam, n: n, shutter: sh, asked: time.Now()}
	return n, nil
}

// queuedShot is a photo for the shooter to take.
type queuedShot struct {
	ctx     context.Context
	cam     Camera
	n       int
	shutter shutter
	asked   time.Time
}

// shooter takes queued photos in turn.
func (c *cameraConn) shooter(q chan queuedShot) {
	for s := range q {
		err := s.cam.Shoot(s.ctx, s.shutter)
		c.mu.Lock()
		if len(c.unfired) > 0 {
			c.unfired = c.unfired[1:]
		}
		if err == nil {
			c.firedAt = append(c.firedAt, time.Now())
			if len(c.firedAt) > 64 {
				c.firedAt = c.firedAt[len(c.firedAt)-64:]
			}
		}
		c.mu.Unlock()
		if err != nil {
			c.landed(s.n)
			c.out(photoEventMsg{Type: "photoFailed", N: s.n})
			c.out(c.batchStatus())
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
// exposure reads the shutter speed set on the camera: how long its shutter
// stays open (0: not known, or Bulb). Kept for the estimate (see exposedLast).
func (c *cameraConn) exposure() time.Duration {
	c.mu.Lock()
	cam := c.cam
	c.mu.Unlock()
	sc, ok := cam.(settingsCamera)
	if !ok {
		return 0
	}
	ss, err := sc.Settings()
	if err != nil {
		return 0
	}
	c.noteSettings(ss)
	return c.exposedLast()
}

// noteSettings keeps the shutter speed from settings read (by the page, too).
func (c *cameraConn) noteSettings(ss []cameraSetting) {
	for _, st := range ss {
		if st.Key == "shutter" {
			d := time.Duration(0)
			if t, err := parseShutter(st.Current); err == nil && t > 0 {
				d = time.Duration(t * float64(time.Second))
			}
			c.mu.Lock()
			c.exposed = d
			c.mu.Unlock()
		}
	}
}

func (c *cameraConn) exposedLast() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exposed
}

// shutterHold is how long a Stop & Shoot shot keeps the rig still after its
// shutter's been fired: the camera takes the command (~1.1 s on the A6600)
// before the exposure's over, so a long one would blur as the rig moves on.
// The shutter time, and a margin; a fast shutter (under 1/15 s) is over
// before the rig, just starting to move, has gone anywhere: no margin.
func shutterHold(exposure time.Duration) time.Duration {
	if exposure < time.Second/15 {
		return exposure
	}
	return exposure + 100*time.Millisecond
}

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
		c.landed(n)
		c.out(photoEventMsg{Type: "photoFailed", N: n, Sample: c.isSample(n)})
		c.out(c.batchStatus())
		c.warn("photo %d: %v", n, err)
	case len(p.JPEG) > 0:
		c.picture(n, p)
	case sim: // a page renders it; if none does, a made-up one
		c.mu.Lock()
		c.pending = n
		if c.simDirs == nil {
			c.simDirs = map[int]string{}
		}
		c.simDirs[n] = p.dir
		c.mu.Unlock()
		sim := cam.(*simCamera)
		var settings map[string]string
		if ss, err := sim.Settings(); err == nil {
			settings = map[string]string{}
			for _, st := range ss {
				settings[st.Key] = st.Current
			}
		}
		c.out(simShootMsg{Type: "simShoot", N: n, Exposure: sim.exposure(), Settings: settings, Blur: sim.blur()})
		gen := c.generation()
		time.AfterFunc(simPictureWait, func() {
			c.mu.Lock()
			rendered := c.simRendered[n]
			c.mu.Unlock()
			if c.current(gen) && !rendered {
				c.picture(n, c.simSave(n, photo{JPEG: simPictureJPEG(n), At: p.At}))
			}
		})
	default: // taken, but nothing to show
		c.landed(n)
		c.out(photoEventMsg{Type: "photoFailed", N: n, Sample: c.isSample(n)})
		c.out(c.batchStatus())
	}
}

// focalLength is the focal length (mm) the camera's photos are taken at, as
// its last one said (0: none yet).
func (c *cameraConn) focalLength() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, sim := c.cam.(*simCamera); sim {
		return s.focal() // its pictures are rendered at it (see rig3d's snapshot)
	}
	return c.focal
}

// generation counts Resets: a photo still coming from before one is dropped
// (its number may be taken again).
func (c *cameraConn) generation() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

func (c *cameraConn) current(gen int) bool { return c.generation() == gen }

// isSample says whether photo n is a sample shot.
func (c *cameraConn) isSample(n int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.samples[n]
	return ok
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
	Type   string `json:"type"`
	N      int    `json:"n"`
	Num    int    `json:"num,omitempty"` // (photoPending: see photoInfo)
	Sample bool   `json:"sample,omitempty"`
}

// delete takes photo n off the timeline, and its files (its RAW beside
// it, too) to the Trash; a picture still coming is trashed as it lands.
func (c *cameraConn) delete(n int) {
	c.mu.Lock()
	files := c.photos[n].Files
	delete(c.photos, n)
	if c.deleted == nil {
		c.deleted = map[int]bool{}
	}
	c.deleted[n] = true
	c.mu.Unlock()
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			if err := trashPhoto(f); err != nil {
				log.Printf("photos: deleting %s: %v", f, err)
			}
		}
	}
	c.out(photoEventMsg{Type: "photoDeleted", N: n})
	c.out(c.batchStatus())
}

// forgetFile takes the photo saved as path off the timeline (its files
// trashed already: see handleBatchPhotoDelete).
func (c *cameraConn) forgetFile(path string) {
	c.mu.Lock()
	n := 0
	for k, p := range c.photos {
		if slices.Contains(p.Files, path) {
			n = k
		}
	}
	if n > 0 {
		delete(c.photos, n)
		if c.deleted == nil {
			c.deleted = map[int]bool{}
		}
		c.deleted[n] = true
	}
	c.mu.Unlock()
	if n > 0 {
		c.out(photoEventMsg{Type: "photoDeleted", N: n})
		c.out(c.batchStatus())
	}
}

// picture sets photo n's picture, adding it to the timeline (once: a later
// copy of the same photo is ignored).
func (c *cameraConn) picture(n int, p photo) bool {
	if c.nameFor != nil && len(p.Files) > 0 {
		if stem := c.nameFor(n); stem != "" { // (a capture's shot: named after its place in the plan)
			p.Files = renameStem(p.Files, stem)
		}
	}
	c.landed(n)
	c.mu.Lock()
	if _, ok := c.photos[n]; ok || c.deleted[n] || n > c.count || n <= c.count-maxPhotos {
		gone := c.deleted[n]
		c.mu.Unlock()
		if gone { // deleted while it was coming: its files go too
			for _, f := range p.Files {
				if _, err := os.Stat(f); err == nil {
					trashPhoto(f)
				}
			}
		}
		return false
	}
	if c.photos == nil {
		c.photos = map[int]photo{}
	}
	if caption, ok := c.samples[n]; ok {
		p.Sample, p.Caption, p.Settings = true, caption, c.sampleSettings[n]
	}
	p.Num = c.nums[n]
	if f, ok := exifFocalLength(p.JPEG); ok {
		c.focal = f
		if p.Settings != nil {
			p.Settings["focal"] = strconv.FormatFloat(f, 'f', -1, 64)
		}
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
	c.out(photoMsg{Type: "photo", N: n, Num: p.Num, File: p.fileName(), At: p.At.UnixMilli(), Sample: p.Sample, Caption: p.Caption, Settings: p.Settings})
	c.out(c.batchStatus())
	if c.onPicture != nil && !p.Sample {
		c.onPicture(n, p)
	}
	return true
}

// pictureOf is photo n with its picture: read from its file if it isn't
// in memory (a resumed capture's photos from before: see resumeIn), and kept.
func (c *cameraConn) pictureOf(n int) (photo, bool) {
	p, ok := c.photo(n)
	if !ok || len(p.JPEG) > 0 || len(p.Files) == 0 {
		return p, ok
	}
	jpg, err := photoJPEG(p.Files[0])
	if err != nil {
		return p, false
	}
	c.mu.Lock()
	if q, ok := c.photos[n]; ok && len(q.JPEG) == 0 {
		q.JPEG = jpg
		c.photos[n] = q
	}
	c.mu.Unlock()
	p.JPEG = jpg
	return p, true
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
		if !p.Sample {
			m.Photos = append(m.Photos, photoInfo{N: n, Num: p.Num, At: p.At.UnixMilli(), File: p.fileName()})
		}
	}
	slices.SortFunc(m.Photos, func(a, b photoInfo) int { return a.N - b.N })
	return m
}

// resumeIn makes the capture in folder dir (an interrupted one: see
// resume.go) the current one: its photos taken (files, in order) on the
// timeline again (their pictures read from the files when they're wanted:
// see pictureOf), the photos to come going into its folder, numbered on.
func (c *cameraConn) resumeIn(dir string, files []string) {
	c.mu.Lock()
	c.photos, c.deleted, c.samples, c.sampleSettings, c.inFlight, c.simRendered, c.simDirs = map[int]photo{}, nil, nil, nil, nil, nil, nil
	c.nums = map[int]int{}
	for i, f := range files {
		n, path := i+1, filepath.Join(dir, f)
		p := photo{Files: []string{path}, Num: n, At: time.Now()}
		if st, err := os.Stat(path); err == nil {
			p.At = st.ModTime()
		}
		c.photos[n], c.nums[n] = p, n
	}
	c.count, c.pending, c.taken = len(files), 0, len(files)
	c.gen++
	c.mu.Unlock()
	batch.Lock()
	batch.dir = dir
	batch.Unlock()
	c.out(c.timeline())
	c.out(c.batchStatus())
}

// reset starts a new capture: the timeline cleared, and a new batch; its
// folder made now with make (New Capture: empty, ready for the photos and
// sample shots to come), else with its first photo (the current capture
// deleted: no empty one left in its place). Its name.
func (c *cameraConn) reset(make bool) string {
	c.mu.Lock()
	c.photos, c.deleted, c.samples, c.sampleSettings, c.inFlight, c.simRendered, c.simDirs = nil, nil, nil, nil, nil, nil, nil
	c.count, c.pending, c.taken, c.nums = 0, 0, 0, nil
	c.gen++
	c.mu.Unlock()
	startBatch(make)
	c.out(c.timeline())
	c.out(c.batchStatus())
	return currentBatch()
}

// simPicture sets simulated photo n's picture, rendered by a page.
// It comes over after simDownload, after the ones before it; whether it's
// taken is known at once (false: not waited for, or a late copy).
func (c *cameraConn) simPicture(n int, jpg []byte) bool {
	if simDownload <= 0 {
		return c.picture(n, c.simSave(n, photo{JPEG: jpg, At: time.Now()}))
	}
	c.mu.Lock()
	if !c.inFlight[n] || c.simRendered[n] {
		c.mu.Unlock()
		return false
	}
	if c.simRendered == nil {
		c.simRendered = map[int]bool{}
	}
	c.simRendered[n] = true
	start := time.Now()
	if c.simLink.After(start) { // the one before is still coming over
		start = c.simLink
	}
	done := start.Add(simDownload)
	c.simLink = done
	gen := c.gen
	c.mu.Unlock()
	time.AfterFunc(time.Until(done), func() {
		if c.current(gen) {
			c.picture(n, c.simSave(n, photo{JPEG: jpg, At: time.Now()}))
		}
	})
	return true
}

// simSave saves simulated photo n's picture as a real camera would its
// JPEG: in the capture's folder (a sample shot's in its samples folder),
// as the shutter went, named SIM_0001.JPG after its number.
func (c *cameraConn) simSave(n int, p photo) photo {
	c.mu.Lock()
	dir, num := c.simDirs[n], c.nums[n]
	delete(c.simDirs, n)
	c.mu.Unlock()
	if dir == "" || len(p.JPEG) == 0 {
		return p
	}
	name := fmt.Sprintf("SIM_%04d.JPG", num)
	if num == 0 { // a sample shot (not counted)
		name = fmt.Sprintf("SIM_S%04d.JPG", n)
	}
	path, err := saveUnique(dir, name, p.JPEG)
	if err != nil {
		c.warn("saving %s: %v", name, err)
		return p
	}
	p.Files = []string{path}
	return p
}

// lastPhoto is the newest photo on the timeline (empty if none), and its
// number.
func (c *cameraConn) lastPhoto() (photo, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	last := 0
	for n, p := range c.photos {
		if !p.Sample {
			last = max(last, n)
		}
	}
	return c.photos[last], last
}

// photoCount is how many photos have been asked for (the last one's number).
func (c *cameraConn) photoCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// landed: photo n's picture came (or it failed): it's no longer in flight.
func (c *cameraConn) landed(n int) {
	c.mu.Lock()
	delete(c.inFlight, n)
	c.mu.Unlock()
}

// firingLate is how long the oldest queued photo has been waiting for its
// shutter (0: none waiting): moving shots hold while it's late (see
// capture.pace), so each is taken at its shot, not further along the path.
func (c *cameraConn) firingLate() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.unfired) == 0 {
		return 0
	}
	return time.Since(c.unfired[0])
}

// backlog is how many photos have been asked for whose pictures haven't come
// yet: queued to be taken, being taken, or coming over. Moving shots slow
// down when it grows (see capture.pace).
// relocate points the timeline's photos' files under dir from (a folder
// renamed) at to.
func (c *cameraConn) relocate(from, to string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	pre := from + string(filepath.Separator)
	for n, p := range c.photos {
		changed := false
		files := append([]string(nil), p.Files...)
		for i, f := range files {
			if strings.HasPrefix(f, pre) {
				files[i] = filepath.Join(to, strings.TrimPrefix(f, pre))
				changed = true
			}
		}
		if changed {
			p.Files = files
			c.photos[n] = p
		}
	}
}

func (c *cameraConn) backlog() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.inFlight)
}

// paceOf is how often (s) camera id keeps up taking photos, as measured (0:
// not measured).
func (c *cameraConn) paceOf(id string) float64 {
	if c.cfg == nil || id == "" {
		return 0
	}
	return c.cfg.get().CameraPace[id]
}

// pace is the connected camera's, as a duration (0: not measured).
func (c *cameraConn) pace() time.Duration {
	c.mu.Lock()
	id := c.id
	c.mu.Unlock()
	return time.Duration(c.paceOf(id) * float64(time.Second))
}

// paceShots is how many photos measurePace takes.
const paceShots = 6

// measurePace finds how often the camera keeps up taking photos: it asks
// for paceShots sample shots at once (the camera takes them as fast as it
// can, each when it's done with the last) and times them coming over, the
// gaps between them once it's under way. The result is kept for the camera
// (moving shots are spaced by it).
func (c *cameraConn) measurePace(ctx context.Context) (float64, error) {
	c.mu.Lock()
	id, cam := c.id, c.cam
	c.mu.Unlock()
	if cam == nil {
		return 0, errNoCamera
	}
	if c.backlog() > 0 {
		return 0, errors.New("the camera is still busy with photos: try again once they're in")
	}
	c.mu.Lock()
	firedBefore := len(c.firedAt)
	c.mu.Unlock()
	for i := 0; i < paceShots; i++ {
		if err := c.take(ctx, false, nil, true); err != nil {
			return 0, err
		}
	}
	var arrived []time.Time
	deadline := time.Now().Add(paceShots * 15 * time.Second)
	for left := paceShots; left > 0; {
		if time.Now().After(deadline) {
			return 0, errors.New("the photos didn't all come over")
		}
		time.Sleep(20 * time.Millisecond)
		for b := c.backlog(); left > b; left-- {
			arrived = append(arrived, time.Now())
		}
	}
	// The slower of the two: the shutters going (each after the camera has
	// the last photo), and the pictures coming over (in bursts, sometimes).
	pace := arrived[len(arrived)-1].Sub(arrived[0]).Seconds() / float64(len(arrived)-1)
	c.mu.Lock()
	fired := append([]time.Time(nil), c.firedAt[max(0, min(firedBefore, len(c.firedAt)-paceShots)):]...)
	c.mu.Unlock()
	if len(fired) >= 2 {
		pace = math.Max(pace, fired[len(fired)-1].Sub(fired[0]).Seconds()/float64(len(fired)-1))
	}
	pace = math.Round(pace*100) / 100
	if c.cfg != nil {
		err := c.cfg.update(func(cf *Config) {
			if cf.CameraPace == nil {
				cf.CameraPace = map[string]float64{}
			}
			cf.CameraPace[id] = pace
		})
		if err != nil {
			return pace, err
		}
	}
	log.Printf("camera: %s keeps up with a photo every %.2f s", id, pace)
	c.out(c.msg())
	return pace, nil
}
