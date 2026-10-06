package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"
)

// shot is one planned photo.
type shot struct {
	Ring      int     `json:"ring"` // row of similar elevation, in shooting order
	Elevation float64 `json:"elevation"`
	Azimuth   float64 `json:"azimuth"`
	Done      bool    `json:"done"`
	// Where the rig really was when the photo was taken (a heavy arm can
	// stop a little short of its goal).
	ActualElevation float64 `json:"actualElevation,omitempty"`
	ActualAzimuth   float64 `json:"actualAzimuth,omitempty"`
}

// planShots spreads p.Photos points as evenly as possible over the band of
// the sphere round the object between m's elevation limits (so the camera
// stays clear of the posts and base), and orders them for shooting.
//
// Placement is a Fibonacci (golden angle) spiral restricted to the band:
// heights on the sphere (sin of the elevation) are evenly spaced, which gives
// every point the same area, and each point turns by the golden angle from
// the previous one, so no direction lines up. The points are then taken in
// rows of similar elevation (so the heavy arm moves little), each row in
// azimuth order, alternating direction so the platform never unwinds a whole
// turn. spacing is the typical angle between neighbouring points.
func planShots(p Plan, m Motion) (shots []shot, rows int, spacing float64, err error) {
	if p.Photos < 1 || p.Photos > 2000 {
		return nil, 0, 0, errors.New("photos must be 1–2000")
	}
	lo, hi := math.Max(-90, m.ElevationMin), math.Min(90, m.ElevationMax)
	if lo >= hi {
		return nil, 0, 0, errors.New("the elevation range in Setup is empty")
	}
	n := p.Photos
	z0, z1 := math.Sin(rad(lo)), math.Sin(rad(hi))
	golden := math.Pi * (3 - math.Sqrt(5)) // ≈137.5°
	pts := make([]shot, n)
	for i := range pts {
		z := z0 + (z1-z0)*(float64(i)+0.5)/float64(n)
		pts[i] = shot{Elevation: round1(deg(math.Asin(z))), Azimuth: round1(wrap180(deg(float64(i) * golden)))}
	}
	// Spacing: the band's area (2π·Δz on a unit sphere) shared by n points.
	area := 2 * math.Pi * (z1 - z0)
	spacing = deg(math.Sqrt(area / float64(n)))
	// Rows: as many as the band's height holds at that spacing.
	rows = max(1, min(n, int(math.Round((hi-lo)/spacing))))
	slices.SortStableFunc(pts, func(a, b shot) int { return cmp.Compare(a.Elevation, b.Elevation) })
	for r := 0; r < rows; r++ {
		row := pts[r*n/rows : (r+1)*n/rows]
		slices.SortStableFunc(row, func(a, b shot) int {
			if r%2 == 1 {
				return cmp.Compare(b.Azimuth, a.Azimuth)
			}
			return cmp.Compare(a.Azimuth, b.Azimuth)
		})
		for k := range row {
			row[k].Ring = r
		}
	}
	return pts, rows, spacing, nil
}

func rad(d float64) float64    { return d * math.Pi / 180 }
func deg(r float64) float64    { return r * 180 / math.Pi }
func round1(v float64) float64 { return math.Round(v*10) / 10 }

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
	rows    int
	spacing float64
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
	Rows    int     `json:"rows"`
	Spacing float64 `json:"spacing"` // degrees between neighbouring shots
}

func (c *capture) msg() captureMsg {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := captureMsg{Type: "capture", Running: c.running, Paused: c.paused, Index: c.index, Total: len(c.shots),
		Shots: append([]shot(nil), c.shots...), Note: c.note, Rows: c.rows, Spacing: c.spacing}
	if c.running {
		m.Elapsed = time.Since(c.started).Seconds()
	}
	return m
}

func (c *capture) send() { c.out(c.msg()) }

// preview lays out a plan without running it (for the rig view).
func (c *capture) preview(p Plan) error {
	shots, rows, spacing, err := planShots(p, c.rig.cfg.get().Motion)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return errors.New("a capture is running")
	}
	c.shots, c.index, c.note, c.rows, c.spacing = shots, 0, "", rows, spacing
	c.mu.Unlock()
	c.send()
	return nil
}

// start runs the plan in the background.
func (c *capture) start(p Plan) error {
	if _, err := c.rig.ready(); err != nil {
		return err
	}
	shots, rows, spacing, err := planShots(p, c.rig.cfg.get().Motion)
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
	c.rows, c.spacing = rows, spacing
	c.resume, c.note, c.started = make(chan struct{}), "", time.Now()
	c.mu.Unlock()
	c.rig.logf("info", "capture started: %d photos in %d rows, about %.0f° apart", len(shots), rows, spacing)
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
