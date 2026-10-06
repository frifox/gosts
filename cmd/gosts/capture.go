package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// shot is one planned photo.
type shot struct {
	Ring      int     `json:"ring"`
	Elevation float64 `json:"elevation"`
	Azimuth   float64 `json:"azimuth"`
	Done      bool    `json:"done"`
	// Where the rig really was when the photo was taken (a heavy arm can
	// stop a little short of its goal).
	ActualElevation float64 `json:"actualElevation,omitempty"`
	ActualAzimuth   float64 `json:"actualAzimuth,omitempty"`
}

// planShots lays out the shots: rings at evenly spaced elevations, each
// evenly round the object. Rings alternate direction so the platform never
// unwinds a whole turn between them.
func planShots(p Plan) ([]shot, error) {
	if p.Rings < 1 || p.Rings > 20 || p.PerRing < 1 || p.PerRing > 360 {
		return nil, errors.New("rings must be 1–20 and photos per ring 1–360")
	}
	var out []shot
	for r := 0; r < p.Rings; r++ {
		e := p.ElevationFrom
		if p.Rings > 1 {
			e += (p.ElevationTo - p.ElevationFrom) * float64(r) / float64(p.Rings-1)
		}
		for i := 0; i < p.PerRing; i++ {
			k := i
			if r%2 == 1 {
				k = p.PerRing - 1 - i
			}
			out = append(out, shot{Ring: r, Elevation: e, Azimuth: wrap180(360 * float64(k) / float64(p.PerRing))})
		}
	}
	return out, nil
}

// capture runs a plan.
type capture struct {
	rig *rig
	out func(any)

	mu      sync.Mutex
	shots   []shot
	index   int // next shot
	running bool
	paused  bool
	note    string
	cancel  context.CancelFunc
	resume  chan struct{}
	started time.Time
}

type captureMsg struct {
	Type    string  `json:"type"` // "capture"
	Running bool    `json:"running"`
	Paused  bool    `json:"paused"`
	Index   int     `json:"index"`
	Total   int     `json:"total"`
	Shots   []shot  `json:"shots"`
	Note    string  `json:"note"`
	Elapsed float64 `json:"elapsed"` // s
}

func (c *capture) msg() captureMsg {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := captureMsg{Type: "capture", Running: c.running, Paused: c.paused, Index: c.index, Total: len(c.shots),
		Shots: append([]shot(nil), c.shots...), Note: c.note}
	if c.running {
		m.Elapsed = time.Since(c.started).Seconds()
	}
	return m
}

func (c *capture) send() { c.out(c.msg()) }

// preview lays out a plan without running it (for the rig view).
func (c *capture) preview(p Plan) error {
	shots, err := planShots(p)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return errors.New("a capture is running")
	}
	c.shots, c.index, c.note = shots, 0, ""
	c.mu.Unlock()
	c.send()
	return nil
}

// start runs the plan in the background.
func (c *capture) start(p Plan) error {
	if _, err := c.rig.ready(); err != nil {
		return err
	}
	shots, err := planShots(p)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		cancel()
		return errors.New("a capture is already running")
	}
	c.shots, c.index, c.running, c.paused, c.cancel = shots, 0, true, false, cancel
	c.resume, c.note, c.started = make(chan struct{}), "", time.Now()
	c.mu.Unlock()
	c.rig.logf("info", "capture started: %d photos (%d rings × %d)", len(shots), p.Rings, p.PerRing)
	c.send()
	go c.run(ctx, p)
	return nil
}

func (c *capture) run(ctx context.Context, p Plan) {
	err := c.loop(ctx, p)
	c.mu.Lock()
	c.running, c.paused, c.cancel = false, false, nil
	done := c.index
	switch {
	case errors.Is(err, context.Canceled):
		c.note = fmt.Sprintf("Stopped after %d of %d photos.", done, len(c.shots))
	case err != nil:
		c.note = fmt.Sprintf("Stopped after %d of %d photos: %v", done, len(c.shots), err)
	default:
		c.note = fmt.Sprintf("Done: %d photos in %s.", done, time.Since(c.started).Round(time.Second))
	}
	note := c.note
	c.mu.Unlock()
	level := "info"
	if err != nil && !errors.Is(err, context.Canceled) {
		level = "error"
	}
	c.rig.logf(level, "capture: %s", note)
	c.send()
}

func (c *capture) loop(ctx context.Context, p Plan) error {
	for {
		c.mu.Lock()
		if c.index >= len(c.shots) {
			c.mu.Unlock()
			return nil
		}
		sh := c.shots[c.index]
		paused, resume := c.paused, c.resume
		c.mu.Unlock()
		if paused {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-resume:
			}
			continue
		}
		e, a := sh.Elevation, sh.Azimuth
		if err := c.rig.moveTo(&e, &a); err != nil {
			return err
		}
		if err := c.rig.waitStill(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(p.SettleMS) * time.Millisecond):
		}
		// The photo: the camera isn't connected yet, so the shot is only
		// recorded with where the rig really is.
		ae, aa := c.rig.where()
		c.mu.Lock()
		c.shots[c.index].Done = true
		c.shots[c.index].ActualElevation, c.shots[c.index].ActualAzimuth = ae, aa
		c.index++
		c.mu.Unlock()
		c.send()
	}
}

func (c *capture) pause(on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return errors.New("no capture is running")
	}
	if c.paused && !on {
		close(c.resume)
		c.resume = make(chan struct{})
	}
	c.paused = on
	go c.send()
	return nil
}

func (c *capture) stop() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
