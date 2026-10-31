package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"os"
	"slices"
	"sync"
	"time"
)

// Measuring the flash's overheat limit (Camera Settings' Measure): sample
// shots, one a second, at a power picked in the AR400 table, till the flash
// stops firing (its overheat protection: then it recycles in 10–15 s), seen
// as photos much darker than the ones before. The flashes it fired till
// then are its limit at that power, kept in the config (FlashLimits), shown
// in the table in place of the manual's.

// FlashLimit is a flash's measured overheat limit at a power.
type FlashLimit struct {
	Flashes int       `toml:"Flashes" json:"flashes"` // fired in a row before it stopped
	At      time.Time `toml:"At" json:"at"`
}

var flashEvery = time.Second // a shot a second (or the camera's pace, if slower; tests: at once)

const (
	flashMaxShots = 800 // the manual's most at any power is 500
	flashDark     = 0.5 // a photo under half the brightness of the bright ones: the flash didn't fire
	flashDarkRun  = 3   // that many dark in a row: it's stopped (one alone: a slow recycle)
	flashSimLimit = 12  // the simulated camera's "flash": dark from this shot on
)

// flashMsg tells the pages how a measurement is going.
type flashMsg struct {
	Type     string  `json:"type"` // "flashMeasure"
	Running  bool    `json:"running"`
	Power    string  `json:"power"`
	Shots    int     `json:"shots"`
	Flashes  int     `json:"flashes"` // bright shots so far
	Bright   float64 `json:"bright"`  // the last shot's brightness (0–255)
	Normal   float64 `json:"normal"`  // the bright ones' (median of the last few)
	Result   int     `json:"result"`  // the limit found (0: none yet)
	Saved    bool    `json:"saved"`   // kept in the config (not for the simulated camera)
	Error    string  `json:"error,omitempty"`
	Canceled bool    `json:"canceled,omitempty"`
}

type flashMeasure struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	last   flashMsg
	out    func(any) // tests: where its messages go (else the pages)
}

func (f *flashMeasure) running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancel != nil
}

// measureFlash starts measuring the limit at power (the table's line).
func (a *app) measureFlash(power string) error {
	if power == "" {
		return errors.New("pick the flash's power in the table first")
	}
	if a.cap.msg().Running {
		return errors.New("a capture is running")
	}
	if !a.camera.connected() {
		return errors.New("no camera connected")
	}
	f := &a.flash
	f.mu.Lock()
	if f.cancel != nil {
		f.mu.Unlock()
		return errors.New("measuring already")
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.mu.Unlock()
	go a.flashLoop(ctx, power)
	return nil
}

// stopMeasure stops a measurement (nothing saved).
func (a *app) stopMeasure() {
	a.flash.mu.Lock()
	if a.flash.cancel != nil {
		a.flash.cancel()
	}
	a.flash.mu.Unlock()
}

func (a *app) flashLoop(ctx context.Context, power string) {
	m := flashMsg{Type: "flashMeasure", Running: true, Power: power}
	send := func() {
		a.flash.mu.Lock()
		a.flash.last = m
		out := a.flash.out
		a.flash.mu.Unlock()
		if out == nil {
			out = a.broadcast
		}
		out(m)
	}
	defer func() {
		a.flash.mu.Lock()
		a.flash.cancel = nil
		a.flash.mu.Unlock()
		m.Running = false
		send()
	}()
	send()
	// The test shots' files: a folder of their own, gone after (a few hundred
	// photos, of a flash going and stopping; the RAWs stay on the card).
	dir, err := os.MkdirTemp("", "gosts-rig-flash-*")
	if err != nil {
		m.Error = err.Error()
		return
	}
	defer os.RemoveAll(dir)
	sim := a.camera.simulated()
	var bright []float64
	dark := 0
	for m.Shots < flashMaxShots {
		start := time.Now()
		n, err := a.camera.takeIn(ctx, true, nil, true, func() string { return dir }, nil)
		if err == nil {
			var p photo
			if p, err = a.camera.waitPicture(ctx, n, 30*time.Second); err == nil {
				m.Bright, err = meanBrightness(p.JPEG)
			}
		}
		if ctx.Err() != nil {
			m.Canceled = true
			return
		}
		if err != nil {
			m.Error = err.Error()
			return
		}
		m.Shots++
		if sim { // (its pictures don't change: the flash made up, stopping after flashSimLimit)
			m.Bright = 140
			if m.Shots > flashSimLimit {
				m.Bright = 20
			}
		}
		recent := bright[max(0, len(bright)-10):]
		m.Normal = median(recent)
		if len(bright) >= 3 && m.Bright < flashDark*m.Normal {
			dark++
		} else {
			dark = 0
			bright = append(bright, m.Bright)
			m.Flashes++
		}
		if dark >= flashDarkRun {
			m.Result = m.Flashes
			break
		}
		send()
		select {
		case <-ctx.Done():
			m.Canceled = true
			return
		case <-time.After(time.Until(start.Add(flashEvery))):
		}
	}
	if m.Result == 0 {
		m.Error = fmt.Sprintf("the flash kept firing for %d shots: no limit found", m.Shots)
		return
	}
	if sim {
		log.Printf("flash: simulated camera: %d flashes at %s (not saved)", m.Result, power)
		return
	}
	err = a.cfg.update(func(c *Config) {
		limits := map[string]FlashLimit{}
		for k, v := range c.FlashLimits {
			limits[k] = v
		}
		limits[power] = FlashLimit{Flashes: m.Result, At: time.Now()}
		c.FlashLimits = limits
	})
	if err != nil {
		m.Error = err.Error()
		return
	}
	m.Saved = true
	log.Printf("flash: %d flashes at %s before it stopped (saved)", m.Result, power)
	a.rig.sendState() // (the config, with it)
}

// waitPicture waits for photo n's picture (taken: coming over).
func (c *cameraConn) waitPicture(ctx context.Context, n int, timeout time.Duration) (photo, error) {
	deadline := time.Now().Add(timeout)
	for {
		if p, ok := c.photo(n); ok {
			return p, nil
		}
		c.mu.Lock()
		coming := c.inFlight[n]
		c.mu.Unlock()
		if !coming {
			return photo{}, fmt.Errorf("photo %d didn't come", n)
		}
		if time.Now().After(deadline) {
			return photo{}, fmt.Errorf("photo %d: no picture after %v", n, timeout)
		}
		select {
		case <-ctx.Done():
			return photo{}, ctx.Err()
		case <-time.After(30 * time.Millisecond):
		}
	}
}

// meanBrightness is a JPEG's mean brightness (0–255): its luma, every 8th
// pixel each way.
func meanBrightness(jpg []byte) (float64, error) {
	img, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		return 0, err
	}
	b := img.Bounds()
	var sum, n float64
	if yc, ok := img.(*image.YCbCr); ok {
		for y := b.Min.Y; y < b.Max.Y; y += 8 {
			for x := b.Min.X; x < b.Max.X; x += 8 {
				sum += float64(yc.Y[yc.YOffset(x, y)])
				n++
			}
		}
	} else {
		for y := b.Min.Y; y < b.Max.Y; y += 8 {
			for x := b.Min.X; x < b.Max.X; x += 8 {
				r, g, bl, _ := img.At(x, y).RGBA()
				sum += (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)) / 257
				n++
			}
		}
	}
	if n == 0 {
		return 0, errors.New("an empty picture")
	}
	return sum / n, nil
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := slices.Clone(v)
	slices.Sort(s)
	return s[len(s)/2]
}
