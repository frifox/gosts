package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
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
	// stop a little short of its goal): nil till its shutter goes (which,
	// with moving shots, is a moment after it's marked Done); 0 is a real
	// 0°.
	ActualElevation *float64 `json:"actualElevation,omitempty"`
	ActualAzimuth   *float64 `json:"actualAzimuth,omitempty"`
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
		return nil, 0, 0, errors.New("the elevation range in Rig Setup is empty")
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
		return linearShots(n, rows, lo, hi, p.Moving), rows, spacing, nil
	}
	if p.Moving {
		return spiralOrder(pts, rows, z0, z1), rows, spacing, nil
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
	return pts, rows, spacing, nil
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

	mu      sync.Mutex
	shots   []shot
	index   int // next shot
	running bool
	paused  bool
	holding bool // moving shots held for the camera to catch up (see pace)
	first   int  // the capture's first photo's number (shot i is photo first+i: see exportAlignment)
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
	// Estimate is about how long (s) the plan takes, not running (see
	// capture.estimate).
	Estimate float64 `json:"estimate,omitempty"`
}

func (c *capture) msg() captureMsg {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := captureMsg{Type: "capture", Running: c.running, Paused: c.paused, Index: c.index, Total: len(c.shots),
		Shots: append([]shot(nil), c.shots...), Note: c.note, Rows: c.rows, Spacing: c.spacing}
	if c.running {
		m.Elapsed = time.Since(c.started).Seconds()
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
	vmax := float64(m.Speed) / stepsPerDegree    // °/s
	acc := float64(m.Acc) * 100 / stepsPerDegree // °/s²
	hop := func(d float64) float64 {             // a move of d degrees: speeding up, at speed, slowing down
		if d <= 0 || vmax <= 0 || acc <= 0 {
			return 0
		}
		if d >= vmax*vmax/acc {
			return d/vmax + vmax/acc
		}
		return 2 * math.Sqrt(d/acc)
	}
	move := func(e0, a0, e1, a1 float64) float64 {
		return math.Max(hop(math.Abs(e1-e0)), hop(math.Abs(wrap180(a1-a0))))
	}
	clamp := func(e float64) float64 { return math.Max(m.ElevationMin, math.Min(m.ElevationMax, e)) }
	e0, a0 := c.rig.where()
	first, last := shots[0], shots[len(shots)-1]
	total := move(e0, a0, clamp(first.Elevation), first.Azimuth) + stillTime
	shot := c.camera.pace().Seconds()
	if shot <= 0 {
		shot = math.Max(1, c.camera.minInterval().Seconds())
	}
	if p.Moving {
		es, as := make([]float64, len(shots)), make([]float64, len(shots))
		for i, s := range shots {
			es[i], as[i] = clamp(s.Elevation), s.Azimuth
		}
		total += newTrajectory(es, as, vmax, acc/2, c.shotGap().Seconds()).duration()
		total += 2 * math.Floor(math.Abs(spiralTurns(shots))/(2*spiralRange)) // a stop and a reset a lap
	} else {
		total += shot // the first photo
		for i := 1; i < len(shots); i++ {
			total += move(clamp(shots[i-1].Elevation), shots[i-1].Azimuth, clamp(shots[i].Elevation), shots[i].Azimuth) +
				stillTime + float64(p.SettleMS)/1000 + shot
		}
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
	c.shots, c.index, c.running, c.paused, c.cancel = shots, 0, true, false, cancel
	c.first = c.camera.photoCount() + 1
	c.rows, c.spacing = rows, spacing
	c.resume, c.note, c.started = make(chan struct{}), "", time.Now()
	c.mu.Unlock()
	how := fmt.Sprintf("in %d rows, Stop & Shoot", rows)
	switch {
	case p.Moving && p.path() == PathRings:
		how = fmt.Sprintf("in %d rows, Continuous", rows)
	case p.Moving:
		how = fmt.Sprintf("on a %d-turn spiral, Continuous", rows)
	}
	c.rig.logf("info", "capture started: %d photos about %.0f° apart, %s", len(shots), spacing, how)
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
	c.waitPictures()
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
	if p.ExportFor != "" {
		go c.exportAlignment(p.ExportFor)
	}
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

// exportAlignment writes the capture's alignment data for app kind (see
// writeExport) beside its photos, once their pictures are all in.
func (c *capture) exportAlignment(kind string) {
	for deadline := time.Now().Add(pictureWait + 10*time.Second); c.camera.backlog() > 0 && time.Now().Before(deadline); {
		time.Sleep(200 * time.Millisecond)
	}
	c.mu.Lock()
	shots, first := append([]shot(nil), c.shots[:c.index]...), c.first
	c.mu.Unlock()
	r := c.rig.cfg.get().Rig
	var photos []exportPhoto
	for i, s := range shots {
		p, ok := c.camera.photo(first + i)
		if !ok || len(p.Files) == 0 || s.ActualElevation == nil || s.ActualAzimuth == nil {
			continue // no file (the simulated camera saves none), or no pose
		}
		photos = append(photos, exportPhoto{Path: p.Files[0], Pose: camPose(r, *s.ActualElevation, *s.ActualAzimuth)})
	}
	if len(photos) == 0 {
		c.rig.logf("info", "capture: no photo files to export alignment data for")
		return
	}
	dir := filepath.Dir(photos[0].Path)
	if err := writeExport(kind, dir, photos, exportIntrinsics{FocalMM: c.camera.focalLength()}); err != nil {
		c.rig.logf("error", "capture: exporting alignment data (%s): %v", kind, err)
		return
	}
	c.rig.logf("info", "capture: alignment data for %s written in %s (%d photos)", kind, dir, len(photos))
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
		if err := c.photo(ctx, true); err != nil {
			return err
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
	if err := c.camera.shoot(ctx, wait, firing); err != nil {
		return fmt.Errorf("camera: %w", err)
	}
	c.mu.Lock()
	c.shots[i].Done = true
	c.index++
	c.mu.Unlock()
	c.send()
	return nil
}

// spiral takes moving shots: the rig goes to the first shot, then runs
// without stopping along the smooth curve through all of them (the one the
// preview shows), each photo taken as the path passes its shot. After a
// pause it carries on from where it stopped, along a curve through the
// shots left.
func (c *capture) spiral(ctx context.Context) error {
	m := c.rig.cfg.get().Motion
	// The servos' limits: speed, and acceleration kept to half the
	// configured one so the speed changes gently.
	maxRate := float64(m.Speed) / stepsPerDegree
	accel := float64(m.Acc) * 100 / stepsPerDegree / 2
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
		tr := newTrajectory(es, as, maxRate, accel, c.shotGap().Seconds())
		err = c.rig.follow(ctx, tr, func(k int) error {
			if k < first {
				return nil
			}
			return c.photo(ctx, false)
		}, func() bool {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.paused
		}, c.pace)
		if errors.Is(err, errPaused) {
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
