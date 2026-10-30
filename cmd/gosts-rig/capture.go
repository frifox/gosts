package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// shot is one planned photo.
type shot struct {
	Ring      int     `json:"ring"` // row of similar elevation, in shooting order
	Elevation float64 `json:"elevation"`
	Azimuth   float64 `json:"azimuth"`
	Done      bool    `json:"done"`
	// Where the rig really was when the photo was taken (a heavy arm can
	// stop a little short of its goal): nil till its shutter goes (which,
	// with moving shots, is a moment after it's marked Done); 0 is a real
	// 0°.
	ActualElevation *float64 `json:"actualElevation,omitempty"`
	ActualAzimuth   *float64 `json:"actualAzimuth,omitempty"`
	// File is its photo's file (in the capture's folder) once the picture
	// came over: it's taken (see resume.go).
	File string `json:"file,omitempty"`
	// Before: taken in an earlier run of the capture (it was interrupted).
	Before bool `json:"before,omitempty"`
	// Num is its place in the plan (1…): its photo's name, capt_001.jpg
	// (see fileStem): the order, whenever it was taken.
	Num int `json:"num,omitempty"`
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
// turn. spacing is the typical angle between neighboring points.
func planShots(p Plan, m Motion) (shots []shot, rows int, spacing float64, err error) {
	if p.Photos < 1 || p.Photos > 2000 {
		return nil, 0, 0, errors.New("photos must be 1–2000")
	}
	lo, hi := math.Max(-90, m.ElevationMin), math.Min(90, m.ElevationMax)
	if lo >= hi {
		return nil, 0, 0, errors.New("the elevation range is empty: set its limits (the Rig card, under the elevation dial)")
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
	if p.path() == PathRings {
		return fromZero(linearShots(n, rows, lo, hi, p.Moving)), rows, spacing, nil
	}
	if p.Moving {
		return fromZero(spiralOrder(pts, rows, z0, z1)), rows, spacing, nil
	}
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
	return fromZero(pts), rows, spacing, nil
}

// fromZero turns the whole plan round so its first shot is at azimuth 0°
// (the platform's front, where it rests): the plans are laid out from
// −180°, so the first move would be half a turn. Every shot turns the same,
// so the moves between them don't change.
func fromZero(shots []shot) []shot {
	if len(shots) == 0 {
		return shots
	}
	d := shots[0].Azimuth
	for i := range shots {
		shots[i].Azimuth = round1(wrap180(shots[i].Azimuth - d))
	}
	return shots
}

// linearShots lays the shots out in rows, each at one elevation, evenly
// apart from lo to hi (bottom up), the elevation changing only between rows.
// Each row gets its share of the n shots by its length round the sphere
// (cos of its elevation: the rows nearer the poles get fewer), at least one,
// evenly round, every other row half a step on (so they don't line up).
// Moving shots go round the same way row after row (the platform never
// turns back); stopping shots go back and forth.
func linearShots(n, rows int, lo, hi float64, moving bool) []shot {
	el := make([]float64, rows)
	w, sum := make([]float64, rows), 0.0
	for r := range el {
		el[r] = lo + (hi-lo)*(float64(r)+0.5)/float64(rows)
		w[r] = math.Max(0.05, math.Cos(rad(el[r])))
		sum += w[r]
	}
	// The shares: each row's by its weight, at least one, the rounding
	// remainders going to the rows that lost most to it.
	count, used := make([]int, rows), 0
	type rem struct {
		r int
		f float64
	}
	var rems []rem
	for r := range count {
		x := float64(n) * w[r] / sum
		count[r] = max(1, int(math.Floor(x)))
		used += count[r]
		rems = append(rems, rem{r, x - math.Floor(x)})
	}
	slices.SortStableFunc(rems, func(a, b rem) int { return cmp.Compare(b.f, a.f) })
	for i := 0; used < n; i = (i + 1) % rows {
		count[rems[i].r]++
		used++
	}
	for i := len(rems) - 1; used > n; i = (i - 1 + rows) % rows { // more rows than shots: take back from the most
		if r := rems[i].r; count[r] > 1 {
			count[r]--
			used--
		}
	}
	var out []shot
	for r := range el {
		k := count[r]
		step := 360 / float64(k)
		off := 0.0
		if r%2 == 1 {
			off = step / 2
		}
		row := make([]shot, k)
		for i := range row {
			az := -180 + off + step*float64(i)
			if !moving && r%2 == 1 { // back the other way
				az = 180 - off - step*float64(i)
			}
			row[i] = shot{Ring: r, Elevation: round1(el[r]), Azimuth: round1(wrap180(az))}
		}
		out = append(out, row...)
	}
	return out
}

// spiralOrder orders the shots along one rising helix of `turns` windings,
// for moving shots: the turntable keeps turning one way while the camera
// climbs a little with every shot, so there are no row ends and no
// reversals. Each shot sits on the winding nearest to it: u (0..turns) is its
// height on the sphere as a fraction of the band, and its place on the helix
// is the winding k plus its azimuth as a fraction of a turn.
func spiralOrder(pts []shot, turns int, z0, z1 float64) []shot {
	key := make(map[*shot]float64, len(pts))
	for i := range pts {
		u := float64(turns) * (math.Sin(rad(pts[i].Elevation)) - z0) / (z1 - z0)
		af := (pts[i].Azimuth + 180) / 360
		k := math.Round(u - af)
		key[&pts[i]] = k + af
	}
	ptrs := make([]*shot, len(pts))
	for i := range pts {
		ptrs[i] = &pts[i]
	}
	slices.SortStableFunc(ptrs, func(a, b *shot) int { return cmp.Compare(key[a], key[b]) })
	out := make([]shot, len(pts))
	for i, p := range ptrs {
		out[i] = *p
		out[i].Ring = int(math.Floor(key[p]))
	}
	return out
}

func rad(d float64) float64    { return d * math.Pi / 180 }
func deg(r float64) float64    { return r * 180 / math.Pi }
func round1(v float64) float64 { return math.Round(v*10) / 10 }

// capture runs a plan.
type capture struct {
	rig    *rig
	camera *cameraConn
	out    func(any)

	mu          sync.Mutex
	shots       []shot
	index       int // next shot
	running     bool
	paused      bool
	holding     bool // moving shots held for the camera to catch up (see pace)
	note        string
	cancel      context.CancelFunc
	resume      chan struct{}
	cooling     time.Time     // paused to cool till then (see cooldown); zero: not
	pausedAt    time.Time     // paused since (zero: not paused)
	exporting   atomic.Int32  // alignment data being written (after the capture: see run)
	pausedTotal time.Duration // paused so far, the pauses over
	hold        time.Duration // Stop & Shoot: kept still this long after each shutter (see shutterHold)
	started     time.Time
	rows        int
	spacing     float64

	// Resuming (see resume.go): the capture's plan and folder (where its
	// state is saved), the photos' shots, whether it's complete, and
	// whether it's one that stopped short, Resume carrying it on.
	plan      Plan
	folder    string
	photoShot map[int]int // photo number → shot
	complete  bool
	resumable bool
	began     time.Time // its first run's start
	saveTimer *time.Timer
	saveMu    sync.Mutex

	// No-flash photos (see noflash.go): the last bright ones' brightness,
	// and the photos to delete as the capture carries on (kept while cooling).
	bright    []float64
	dropLater []int
	// How many photos the flash lasts before one comes out dark: those
	// since the last (or the start: darkSince), and each run before
	// (darkRuns, the latest darkRunsKept: across captures, the same flash).
	darkSince int
	darkRuns  []int
}

type captureMsg struct {
	Type    string `json:"type"` // "capture"
	Running bool   `json:"running"`
	Paused  bool   `json:"paused"`
	Index   int    `json:"index"`
	Total   int    `json:"total"`
	Shots   []shot `json:"shots"`
	Note    string `json:"note"`
	// CoolUntil: paused to cool (see Cooldown) till then (ms since 1970).
	CoolUntil int64   `json:"coolUntil,omitempty"`
	Elapsed   float64 `json:"elapsed"` // s
	Active    float64 `json:"active"`  // s of it not paused (the pace of the photos: see the page's time left)
	// DarkEvery: how many photos the flash lasts before one comes out dark,
	// on average so far (Cooldown.Dark; 0: none dark yet), and DarkSince,
	// how many since the last: the pauses still to come (see the page's time
	// left).
	DarkEvery float64 `json:"darkEvery,omitempty"`
	DarkSince int     `json:"darkSince,omitempty"`
	Rows      int     `json:"rows"`
	Spacing   float64 `json:"spacing"` // degrees between neighboring shots
	// Estimate is about how long (s) the plan takes, not running (see
	// capture.estimate).
	Estimate float64 `json:"estimate,omitempty"`
	// Resumable: an interrupted capture, its plan kept: the Capture button
	// is Resume (the shots left), New Capture starting afresh.
	Resumable bool `json:"resumable,omitempty"`
}

func (c *capture) msg() captureMsg {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := captureMsg{Type: "capture", Running: c.running, Paused: c.paused, Index: c.index, Total: len(c.shots),
		Shots: append([]shot(nil), c.shots...), Note: c.note, Rows: c.rows, Spacing: c.spacing, Resumable: c.resumable && !c.running}
	if c.running {
		m.Elapsed = time.Since(c.started).Seconds()
		m.Active = m.Elapsed - c.pausedFor().Seconds()
	}
	if !c.cooling.IsZero() {
		m.CoolUntil = c.cooling.UnixMilli()
	}
	if c.rig.cfg.get().Cooldown.Dark {
		m.DarkEvery, m.DarkSince = c.darkEvery(), c.darkSince
	}
	c.mu.Unlock()
	if !m.Running { // (outside the lock: it asks the rig and the camera)
		m.Estimate = math.Round(c.estimate(c.rig.cfg.get().Plan, m.Shots))
	}
	c.mu.Lock()
	return m
}

// stillTime is about how long the rig takes to be seen to have stopped
// (waitStill: three still readings 50 ms apart, and the last bit of
// slowing down), for the estimate.
const stillTime = 0.4

// shotGap is how close together (s) moving shots may be: the camera's
// measured pace and a little (see measurePace), else as it's been going.
// photoGap is how long (s) a moving capture takes from photo to photo: the
// seconds a photo set, or the camera's pace if that's longer.
func (c *capture) photoGap(m Motion) float64 {
	if m.byRate() { // (the servos' speed sets the pace: as close as the camera keeps up)
		return c.shotGap().Seconds()
	}
	return math.Max(m.SecPerPhoto, c.shotGap().Seconds())
}

// moveTime is how long (s) a Stop & Shoot move takes: the seconds a photo
// set, less the settle time and the photo (the camera's pace, and the
// shutter hold); at least moveTimeMin.
func (c *capture) moveTime(m Motion, settleMS int) float64 {
	shot := c.camera.pace().Seconds()
	if shot <= 0 {
		shot = math.Max(1, c.camera.minInterval().Seconds())
	}
	c.mu.Lock()
	hold, running := c.hold, c.running
	c.mu.Unlock()
	if !running { // (the estimate, before: the shutter speed last read)
		hold = shutterHold(c.camera.exposedLast())
	}
	return math.Max(moveTimeMin, m.SecPerPhoto-float64(settleMS)/1000-shot-hold.Seconds()-stillTime)
}

// moveTimeMin: the quickest a Stop & Shoot move is made to take.
const moveTimeMin = 0.3

// capRates is the fastest (°/s) a capture's moves go, elevation and
// azimuth: each axis's speed set, or (seconds a photo) as fast as they're
// asked to (the pace from photoGap).
func capRates(m Motion) (e, a float64) {
	if m.byRate() {
		return float64(m.Speed) / stepsPerDegree, float64(m.AzimuthSpeed) / stepsPerDegree
	}
	return float64(servoTop) / stepsPerDegree, float64(servoTop) / stepsPerDegree
}

// accels is each axis's acceleration set (°/s²), elevation and azimuth.
func accels(m Motion) (e, a float64) {
	return float64(m.Acc) * 100 / stepsPerDegree, float64(m.AzimuthAcc) * 100 / stepsPerDegree
}

// timedSpeed is the speed (step/s) that moves d degrees in sec seconds,
// within what the servos are asked for.
func timedSpeed(d, sec float64) int {
	return max(20, min(servoTop, int(math.Abs(d)*stepsPerDegree/sec)))
}

func (c *capture) shotGap() time.Duration {
	if p := c.camera.pace(); p > 0 {
		return p * 11 / 10
	}
	return c.camera.minInterval()
}

// estimate is about how long (s) a capture of these shots takes, from what's
// known: the move to the first shot from where the rig is, then for moving
// shots the timed path itself (the camera's pace in it) and the platform's
// turn-count resets, for stopping ones each hop (at the servos' speed and
// acceleration), coming to a stop, the settle time and the photo (the
// camera's pace); then the return to 0°/0°. Not what it can't know: the
// camera falling behind, the arm trailing its path.
func (c *capture) estimate(p Plan, shots []shot) float64 {
	if len(shots) == 0 {
		return 0
	}
	m := c.rig.cfg.get().Motion
	vE, vA := float64(m.Speed)/stepsPerDegree, float64(m.AzimuthSpeed)/stepsPerDegree // °/s
	accE, accA := accels(m)                                                           // °/s²
	hop := func(d, vmax, acc float64) float64 {                                       // a move of d degrees: speeding up, at speed, slowing down
		if d <= 0 || vmax <= 0 || acc <= 0 {
			return 0
		}
		if d >= vmax*vmax/acc {
			return d/vmax + vmax/acc
		}
		return 2 * math.Sqrt(d/acc)
	}
	move := func(e0, a0, e1, a1 float64) float64 {
		return math.Max(hop(math.Abs(e1-e0), vE, accE), hop(math.Abs(wrap180(a1-a0)), vA, accA))
	}
	clamp := func(e float64) float64 { return math.Max(m.ElevationMin, math.Min(m.ElevationMax, e)) }
	topE, topA := capRates(m)
	mt := c.moveTime(m, p.SettleMS)
	timed := func(e0, a0, e1, a1 float64) float64 { // a Stop & Shoot move: moveTime, or longer if the servos can't
		if m.byRate() {
			return move(e0, a0, e1, a1) // (at the servos' speed)
		}
		return math.Max(mt, math.Max(hop(math.Abs(e1-e0), topE, accE), hop(math.Abs(wrap180(a1-a0)), topA, accA))) // (at the servos' top speed)
	}
	e0, a0 := c.rig.where()
	first, last := shots[0], shots[len(shots)-1]
	total := move(e0, a0, clamp(first.Elevation), first.Azimuth) + stillTime
	shot := c.camera.pace().Seconds()
	if shot <= 0 {
		shot = math.Max(1, c.camera.minInterval().Seconds())
	}
	hold := shutterHold(c.camera.exposedLast()).Seconds() // (the shutter speed last read)
	if p.Moving {
		es, as := make([]float64, len(shots)), make([]float64, len(shots))
		for i, s := range shots {
			es[i], as[i] = clamp(s.Elevation), s.Azimuth
		}
		total += newTrajectory(es, as, topE, topA, accE/2, accA/2, c.photoGap(m)).duration()
		total += 2 * math.Floor(math.Abs(spiralTurns(shots))/(2*spiralRange)) // a stop and a reset a lap
	} else {
		total += shot + hold // the first photo
		for i := 1; i < len(shots); i++ {
			total += timed(clamp(shots[i-1].Elevation), shots[i-1].Azimuth, clamp(shots[i].Elevation), shots[i].Azimuth) +
				stillTime + float64(p.SettleMS)/1000 + shot + hold
		}
	}
	// The pauses to cool (after every so many photos, not the last); or,
	// pausing after a no-flash photo, as many as the flash has needed (as
	// often as it's lasted so far: see darkEvery), each with the dark photo.
	cd := c.rig.cfg.get().Cooldown
	if every := cd.every(); every > 0 {
		total += float64((len(shots)-1)/every) * cd.Minutes * 60
	} else if cd.Dark {
		c.mu.Lock()
		n := c.darkEvery()
		c.mu.Unlock()
		total += darkPauses(len(shots), n, 0) * (cd.DarkMinutes*60 + shot + hold)
	}
	return total + move(clamp(last.Elevation), last.Azimuth, 0, 0)
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
	if c.resumable { // (its own plan and shots kept, to resume)
		c.mu.Unlock()
		c.send()
		return nil
	}
	c.shots, c.index, c.note, c.rows, c.spacing = shots, 0, "", rows, spacing
	c.mu.Unlock()
	c.send()
	return nil
}

// start runs the plan in the background. If it can't, why is the note
// too (so it stays on the page, not just a moment's error).
func (c *capture) start(p Plan) error {
	err := c.begin(p)
	if err != nil {
		c.mu.Lock()
		running := c.running
		if !running {
			c.note = err.Error()
		}
		c.mu.Unlock()
		if !running {
			c.send()
		}
	}
	return err
}

func (c *capture) begin(p Plan) error {
	if _, err := c.rig.ready(); err != nil {
		return err
	}
	if !c.camera.connected() {
		return errors.New("connect a camera first (the simulator will do for a try)")
	}
	c.mu.Lock()
	resuming := c.resumable && c.index < len(c.shots)
	c.mu.Unlock()
	if resuming {
		return c.resumeRun()
	}
	shots, rows, spacing, err := planShots(p, c.rig.cfg.get().Motion)
	if err != nil {
		return err
	}
	if p.Moving {
		// The platform isn't unwound between captures: if the spiral would
		// fit its servo's turns from where it is turning the other way (the
		// same shots, mirrored), it does, rather than unwind on the way (see
		// spiral).
		at, err := c.rig.platformTurns()
		if err != nil {
			return err
		}
		if spiralFit(at, shots) < len(shots) && spiralFit(at, mirrored(shots)) > spiralFit(at, shots) {
			c.rig.logf("info", "capture: the platform is %.1f turns round, so the spiral turns the other way this time", at)
			shots = mirrored(shots)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		cancel()
		return errors.New("a capture is already running")
	}
	// (a folder with a capture's photos in it already: kept, taken before;
	// this one's numbered on from them)
	prior, top := priorShots(batchFolder())
	for i := range shots {
		shots[i].Num = top + i + 1 // (its place in the plan: its photo's name, see fileStem)
	}
	c.shots, c.index, c.running, c.paused, c.cancel = append(prior, shots...), len(prior), true, false, cancel
	c.pausedAt, c.pausedTotal = time.Time{}, 0
	c.rows, c.spacing = rows, spacing
	c.resume, c.note, c.started = make(chan struct{}), "", time.Now()
	c.plan, c.folder, c.photoShot, c.complete, c.resumable, c.began = p, batchFolder(), map[int]int{}, false, false, c.started
	c.bright, c.darkSince = nil, 0
	c.mu.Unlock()
	c.saveNow() // (its plan and shots, in its folder: resumable from the start)
	how := fmt.Sprintf("in %d rows, Stop & Shoot", rows)
	switch {
	case p.Moving && p.path() == PathRings:
		how = fmt.Sprintf("in %d rows, Continuous", rows)
	case p.Moving:
		how = fmt.Sprintf("on a %d-turn spiral, Continuous", rows)
	}
	c.rig.logf("info", "capture started: %d photos about %.0f° apart, %s", len(shots), spacing, how)
	if len(prior) > 0 || top > 0 {
		c.rig.logf("info", "capture: adding to the %d photos in its folder already (numbered on from capt_%03d)", len(prior), top)
	}
	if at, err := c.rig.platformTurns(); err == nil {
		c.rig.logf("info", "capture: the platform is %.2f turns round (its servo reaches ±7.5)", at)
	}
	if !p.Moving {
		// Stop & Shoot keeps still while the shutter's open (see shutterHold).
		exp := c.camera.exposure()
		c.mu.Lock()
		c.hold = shutterHold(exp)
		c.mu.Unlock()
		if exp >= time.Second/15 {
			c.rig.logf("info", "capture: holding still %v after each shutter (the shutter's open %v)", c.hold.Round(time.Millisecond), exp.Round(time.Millisecond))
		} else if exp == 0 {
			c.rig.logf("info", "capture: the shutter speed isn't known (Bulb?): no holding still after each shutter")
		}
	}
	c.send()
	go c.run(ctx, p)
	return nil
}

func (c *capture) run(ctx context.Context, p Plan) {
	err := c.loop(ctx, p)
	// The capture is done once the rig is back at 0°/0° and every photo's
	// pictures are in: till then it runs (a note says which it's waiting for).
	if err == nil { // all taken: back to 0°/0°
		c.rig.logf("info", "capture: all photos taken, returning to 0°/0°")
		c.setNote("Returning to 0°/0°…")
		if herr := c.rig.home(); herr != nil {
			c.rig.logf("error", "capture: couldn't return to 0°/0°: %v", herr)
		} else if werr := c.rig.waitStill(ctx); werr != nil && !errors.Is(werr, context.Canceled) {
			c.rig.logf("error", "capture: returning to 0°/0°: %v", werr)
		}
	}
	c.resetTurnsAfter()
	c.waitPictures()
	c.mu.Lock()
	c.dropNow() // (no-flash photos kept while cooling, stopped then: not a shot's)
	c.running, c.paused, c.cancel, c.cooling = false, false, nil, time.Time{}
	// Complete: every shot's photo saved. Else it's resumable: the shots
	// taken first (marked Before), the rest after, in their order, Resume
	// carrying on with them.
	c.complete = true
	for _, s := range c.shots {
		c.complete = c.complete && s.File != ""
	}
	c.resumable = !c.complete
	if c.resumable {
		c.shots, c.index = takenFirst(c.shots)
	}
	if c.saveTimer != nil {
		c.saveTimer.Stop()
		c.saveTimer = nil
	}
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
	c.saveNow() // (its state, and its alignment data: all the photos taken)
	c.send()
}

// resetTurnsAfter starts the platform's turn count afresh where it is once
// a capture's done or stopped (as between a spiral's laps: no move), so a
// move after goes the short way rather than unwinding the turns the capture
// made. Once the rig's still; not when it's within a turn already.
func (c *capture) resetTurnsAfter() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.rig.waitStill(ctx); err != nil {
		return // (no rig, or still moving: left as it is)
	}
	at, err := c.rig.platformTurns()
	if err != nil || math.Abs(at) < 1 {
		return
	}
	c.setNote("Resetting the platform's turn count…")
	if err := c.rig.resetPlatformTurns(); err != nil {
		c.rig.logf("error", "capture: couldn't reset the platform's turn count (it's %.1f turns round): %v", at, err)
	} else {
		c.rig.logf("info", "capture: the platform's turn count reset (it was %.1f turns round)", at)
	}
	c.setNote("")
}

// resumeRun carries on the capture's plan with the shots left (the plan as
// it was: c.plan).
func (c *capture) resumeRun() error {
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		cancel()
		return errors.New("a capture is already running")
	}
	p := c.plan
	c.running, c.paused, c.cancel, c.resumable = true, false, cancel, false
	c.pausedAt, c.pausedTotal = time.Time{}, 0
	c.resume, c.note, c.started = make(chan struct{}), "", time.Now()
	if c.photoShot == nil {
		c.photoShot = map[int]int{}
	}
	c.bright, c.darkSince = nil, 0
	left, total := len(c.shots)-c.index, len(c.shots)
	c.mu.Unlock()
	c.seedBright() // (its photos before: what a new one's compared with)
	c.rig.logf("info", "capture resumed: %d of %d photos left", left, total)
	if !p.Moving {
		exp := c.camera.exposure()
		c.mu.Lock()
		c.hold = shutterHold(exp)
		c.mu.Unlock()
	}
	c.send()
	go c.run(ctx, p)
	return nil
}

// takenFirst orders shots taken first (marked Before: done in a run before),
// then the rest (not done), each in its order, and says how many are taken.
func takenFirst(shots []shot) ([]shot, int) {
	var taken, left []shot
	for _, s := range shots {
		if s.File != "" {
			s.Done, s.Before = true, true
			taken = append(taken, s)
		} else {
			s.Done, s.Before, s.ActualElevation, s.ActualAzimuth = false, false, nil, nil
			left = append(left, s)
		}
	}
	return append(taken, left...), len(taken)
}

// waitPictures waits for the photos asked for to come over from the camera
// (each gives up after pictureWait at most), saying how many are left.
func (c *capture) waitPictures() {
	shown := -1
	for deadline := time.Now().Add(pictureWait + 10*time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		n := c.camera.backlog()
		if n == 0 {
			return
		}
		if n != shown {
			shown = n
			what := "photos"
			if n == 1 {
				what = "photo"
			}
			c.setNote(fmt.Sprintf("Downloading the last %d %s…", n, what))
		}
	}
	c.rig.logf("error", "capture: %d photos didn't come over from the camera", c.camera.backlog())
}

// spiralRange is how far (turns) the platform servo's goals reach either way.
const spiralRange = 30719.0/4096 - 0.05

// spiralFit is how many of shots the spiral can take starting from `at`
// turns (each reached the short way from the one before; the first from
// where the platform is), staying within the platform servo's goal range.
func spiralFit(at float64, shots []shot) int {
	deg := at * 360
	for i, s := range shots {
		deg += wrap180(s.Azimuth - deg)
		if math.Abs(deg/360) > spiralRange {
			return i
		}
	}
	return len(shots)
}

// unwindTurns is how many whole turns to unwind the platform, from `at`
// turns, before a spiral turning `dir` (+1 or −1): back as far as the
// servo's range goes, for the longest lap next (0: no room gained).
func unwindTurns(at float64, dir int) int {
	return int(math.Floor(float64(dir)*at + spiralRange))
}

// mirrored is the same shots with the azimuths mirrored, so the spiral turns
// the other way.
func mirrored(shots []shot) []shot {
	out := append([]shot(nil), shots...)
	for i := range out {
		out[i].Azimuth = wrap180(-out[i].Azimuth)
	}
	return out
}

// spiralTurns is how far the platform turns along the shots (in turns,
// signed like azimuth), each hop the short way.
func spiralTurns(shots []shot) float64 {
	t := 0.0
	for i := 1; i < len(shots); i++ {
		t += wrap180(shots[i].Azimuth - shots[i-1].Azimuth)
	}
	return t / 360
}

func (c *capture) loop(ctx context.Context, p Plan) error {
	if p.Moving {
		return c.spiral(ctx)
	}
	var lastShot time.Time // (see Motion.SecPerPhoto)
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
		// Timed: the move takes what the seconds a photo leave it (each
		// axis at its own speed, so both arrive together).
		m := c.rig.cfg.get().Motion
		if m.byRate() { // (the servos' speed set: at it)
			if err := c.rig.moveTo(&e, &a); err != nil {
				return err
			}
		} else {
			e0, a0 := c.rig.where()
			mt := c.moveTime(m, p.SettleMS)
			ec := math.Max(m.ElevationMin, math.Min(m.ElevationMax, e))
			if err := c.rig.moveToAt(&e, &a, timedSpeed(ec-e0, mt), timedSpeed(wrap180(a-a0), mt)); err != nil {
				return err
			}
		}
		if err := c.rig.waitStill(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(p.SettleMS) * time.Millisecond):
		}
		// The seconds a photo, from the last one's shutter: waited out if
		// the move (timed by an estimate of the rest) came in early.
		if !lastShot.IsZero() && !m.byRate() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Until(lastShot.Add(time.Duration(m.SecPerPhoto * float64(time.Second))))):
			}
		}
		lastShot = time.Now()
		if err := c.photo(ctx, true); err != nil {
			return err
		}
		c.mu.Lock()
		hold := c.hold
		c.mu.Unlock()
		if hold > 0 { // the shutter still open, maybe
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(hold):
			}
		}
	}
}

// photo takes the next shot with the camera, recording where the rig really
// is as the shutter goes (read fresh then: moving shots are taken in
// motion). With wait it returns once the shutter has fired (stopping shots:
// the rig must keep still till then); without, at once: the camera takes it
// in turn while the rig moves on (moving shots, spaced for the camera; see
// spiral), and the shot's pose is filled in as its shutter goes.
func (c *capture) photo(ctx context.Context, wait bool) error {
	c.mu.Lock()
	i := c.index
	c.mu.Unlock()
	firing := func() {
		ae, aa := c.rig.where()
		if fe, fa, err := c.rig.angles(); err == nil {
			ae, aa = fe, fa
		}
		c.mu.Lock()
		if i < len(c.shots) {
			c.shots[i].ActualElevation, c.shots[i].ActualAzimuth = &ae, &aa
		}
		c.mu.Unlock()
		if !wait {
			c.send() // the page has the shot already; now where it was taken
		}
	}
	n, err := c.camera.takeIn(ctx, wait, firing, false, nil, func(n int) {
		c.mu.Lock()
		if c.photoShot != nil {
			c.photoShot[n] = i // (its picture's file: the shot's, named after it, when it comes; see pictured, fileStem)
		}
		c.mu.Unlock()
	})
	if err != nil {
		return fmt.Errorf("camera: %w", err)
	}
	c.mu.Lock()
	if c.index != i { // (gone back to an earlier shot meanwhile, a no-flash photo's: this one goes too)
		delete(c.photoShot, n)
		c.dropLater = append(c.dropLater, n)
		c.mu.Unlock()
		return nil
	}
	c.shots[i].Done = true
	c.index++
	n, total := c.index, len(c.shots)
	c.mu.Unlock()
	if every := c.rig.cfg.get().Cooldown.every(); every > 0 && n%every == 0 && n < total {
		c.cooldown()
	}
	c.send()
	return nil
}

// cooldown pauses the capture for the Cooldown's minutes (the flash
// cooling), then carries on by itself; Resume carries on sooner. Moving
// shots stop where they are, the photos asked for already still taken.
func (c *capture) cooldown() {
	d := time.Duration(c.rig.cfg.get().Cooldown.Minutes * float64(time.Minute))
	c.mu.Lock()
	n := c.index
	c.mu.Unlock()
	c.rig.logf("info", "capture: pausing %v after %d photos, to let the flash cool", d.Round(time.Second), n)
	c.pauseFor(d)
}

// pauseFor pauses the capture for d (cooling the flash), then carries on
// by itself (Resume sooner).
func (c *capture) pauseFor(d time.Duration) {
	until := time.Now().Add(d)
	c.mu.Lock()
	c.paused, c.cooling = true, until
	c.setPaused(true)
	c.mu.Unlock()
	time.AfterFunc(d, func() {
		c.mu.Lock()
		mine := c.running && c.paused && c.cooling.Equal(until) // not resumed (or stopped) meanwhile
		c.mu.Unlock()
		if mine {
			c.pause(false)
		}
	})
}

// spiral takes moving shots: the rig goes to the first shot, then runs
// without stopping along the smooth curve through all of them (the one the
// preview shows), each photo taken as the path passes its shot. After a
// pause it carries on from where it stopped, along a curve through the
// shots left. The speed (and the rest of Motion) is read for each curve:
// changed on the way, the rig stops and carries on along a new curve at it,
// as after a pause.
func (c *capture) spiral(ctx context.Context) error {
	c.mu.Lock()
	dir := 1 // which way the spiral turns the platform
	if spiralTurns(c.shots) < 0 {
		dir = -1
	}
	c.mu.Unlock()
	for {
		c.mu.Lock()
		idx, left := c.index, append([]shot(nil), c.shots[c.index:]...)
		paused, resume := c.paused, c.resume
		c.mu.Unlock()
		if len(left) == 0 {
			return nil
		}
		if paused {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-resume:
			}
			continue
		}
		m := c.rig.cfg.get().Motion
		// The servos' limits: speed, and acceleration kept to half the
		// configured one so the speed changes gently.
		rateE, rateA := capRates(m) // (seconds a photo: the pace set by photoGap)
		accE, accA := accels(m)
		// The platform servo's goals reach only so far: the spiral goes
		// as far as fits (a lap), then the platform unwinds whole turns to
		// the same angle and the spiral carries on from there.
		at, err := c.rig.platformTurns()
		if err != nil {
			return err
		}
		fit := spiralFit(at, left)
		if fit == 0 {
			// The rig stops; the platform servo's turn count is reset where
			// it is (quick); if that can't be done, the platform unwinds
			// instead (slower: whole turns back to the same angle).
			c.setNote("Resetting the platform's turn count…")
			if err := c.rig.stop(); err != nil {
				return err
			}
			if err := c.rig.waitStill(ctx); err != nil {
				return err
			}
			rerr := c.rig.resetPlatformTurns()
			if rerr == nil {
				c.rig.logf("info", "capture: the platform's turn count reset (it was %.1f turns round), after %d of %d photos", at, idx, idx+len(left))
				c.setNote("")
				continue
			}
			n := unwindTurns(at, dir)
			if n < 1 {
				return fmt.Errorf("the platform is %.1f turns round, its count couldn't be reset (%v), and there's no room to unwind", at, rerr)
			}
			c.rig.logf("info", "capture: couldn't reset the platform's turn count (%v): unwinding it %d turns instead", rerr, n)
			c.setNote(fmt.Sprintf("Unwinding the platform %d turns…", n))
			err := c.rig.unwindPlatform(ctx, -dir*n)
			c.setNote("")
			if err != nil {
				return err
			}
			continue
		}
		left = left[:fit]
		var es, as []float64
		first := 0 // the point that is shot idx
		if idx == 0 {
			e, a := left[0].Elevation, left[0].Azimuth
			if err := c.rig.moveTo(&e, &a); err != nil {
				return err
			}
			if err := c.rig.waitStill(ctx); err != nil {
				return err
			}
		} else { // resuming: from where the rig is
			e, a, err := c.rig.angles()
			if err != nil {
				return err
			}
			es, as, first = []float64{e}, []float64{a}, 1
		}
		for _, s := range left {
			es = append(es, math.Max(m.ElevationMin, math.Min(m.ElevationMax, s.Elevation)))
			as = append(as, s.Azimuth)
		}
		// Shots no closer together than the camera keeps up with: its measured
		// pace (see measurePace) and a little, else as it's been going (an
		// estimate from its timings, on the safe side). If it falls behind
		// anyway, the path slows (see pace).
		tr := newTrajectory(es, as, rateE, rateA, accE/2, accA/2, c.photoGap(m))
		err = c.rig.follow(ctx, tr, func(k int) error {
			if k < first {
				return nil
			}
			return c.photo(ctx, false)
		}, func() bool {
			c.mu.Lock()
			paused := c.paused
			c.mu.Unlock()
			return paused || c.rig.cfg.get().Motion != m // (changed: a new curve)
		}, c.pace)
		if errors.Is(err, errPaused) {
			c.mu.Lock()
			paused := c.paused
			c.mu.Unlock()
			if now := c.rig.cfg.get().Motion; !paused && now != m {
				c.rig.logf("info", "capture: the motion changed (speed %.0f°/s): carrying on along a new path", float64(now.Speed)/stepsPerDegree)
			}
			continue
		}
		if err == nil { // a lap done: on to the next (after unwinding), if there are shots left
			c.mu.Lock()
			more := c.index < len(c.shots)
			c.mu.Unlock()
			if more {
				continue
			}
		}
		return err
	}
}

// paceHold, paceSlow, paceGo: the photos waiting for the camera (asked for,
// their pictures not come yet) at which moving shots hold still, go at half
// speed, and go on again after holding. The camera buffers a few; past that
// it starts ignoring the shutter.
const paceHold, paceSlow, paceGo = 4, 3, 2

// fireLate is how long a photo may wait for its shutter (the camera busy
// with the last) before moving shots hold for it.
const fireLate = 150 * time.Millisecond

// pace is how fast moving shots' path goes (1: as planned) for how far the
// camera is behind: held while a photo's shutter hasn't gone yet (so each
// is taken at its shot, not further along), and at paceHold photos waiting
// for their pictures till it's down to paceGo; half speed at paceSlow.
func (c *capture) pace() float64 {
	if c.camera.firingLate() > fireLate {
		return 0
	}
	b := c.camera.backlog()
	c.mu.Lock()
	started := b >= paceHold && !c.holding
	switch {
	case started:
		c.holding = true
	case c.holding && b <= paceGo:
		c.holding = false
	}
	holding := c.holding
	c.mu.Unlock()
	if started {
		c.rig.logf("info", "capture: %d photos waiting for the camera: holding till it catches up", b)
	}
	switch {
	case holding:
		return 0
	case b >= paceSlow:
		return 0.5
	}
	return 1
}

// setNote says something on the page while the capture runs (e.g. that the
// platform is unwinding); "" clears it.
func (c *capture) setNote(note string) {
	c.mu.Lock()
	c.note = note
	c.mu.Unlock()
	c.send()
}

func (c *capture) pause(on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return errors.New("no capture is running")
	}
	if c.paused && !on {
		c.dropNow() // (no-flash photos: gone just before it carries on)
		close(c.resume)
		c.resume = make(chan struct{})
		c.cooling = time.Time{} // (cooling over, or cut short)
	}
	c.paused = on
	c.setPaused(on)
	go c.send()
	return nil
}

// isRunning is whether a capture is running (cheap: not msg, which
// estimates).
func (c *capture) isRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// setPaused keeps the time paused (c.mu held).
func (c *capture) setPaused(on bool) {
	switch {
	case on && c.pausedAt.IsZero():
		c.pausedAt = time.Now()
	case !on && !c.pausedAt.IsZero():
		c.pausedTotal += time.Since(c.pausedAt)
		c.pausedAt = time.Time{}
	}
}

// pausedFor is how long the capture has been paused, all told (c.mu held).
func (c *capture) pausedFor() time.Duration {
	d := c.pausedTotal
	if !c.pausedAt.IsZero() {
		d += time.Since(c.pausedAt)
	}
	return d
}

func (c *capture) stop() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
