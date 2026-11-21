package main

import (
	"fmt"
	"math"
	"path/filepath"
	"time"
)

// Auto-pause after a no-flash photo (Cooldown.Dark): each of a capture's
// photos is looked at as it comes over, and one much darker than the ones
// before it (under flashDark of theirs: the flash didn't fire, overheated)
// pauses the capture for DarkMinutes; then it carries on from that photo's
// shot, the photo taken again in its place. The dark photo, and any taken
// after it before the pause took hold, stay while it cools (to be seen),
// and are deleted just before it carries on (to the Trash, off the timeline:
// as the timeline's bin does; stopped instead: then). A photo taken again
// that comes out dark too is caught the same way: another pause, another try.

// noFlashRecent is how many of the last bright photos set the brightness a
// photo is compared with (their median): from the one before on (resumed:
// the capture's last ones, see seedBright).
const noFlashRecent = 10

// checkFlash looks at shot i's photo n (just come over, its file saved) for
// a flash that didn't fire. (In the background: it's decoded.)
func (c *capture) checkFlash(n, i int, p photo) {
	cd := c.rig.cfg.get().Cooldown
	if !cd.Dark || len(p.JPEG) == 0 {
		return
	}
	l, err := photoLight(p.JPEG, cd.masked()) // (the camera's preview: much quicker)
	if err != nil {
		return
	}
	c.mu.Lock()
	if !c.running || i >= len(c.shots) || c.photoShot[n] != i {
		c.mu.Unlock()
		return // (stopped, or deleted meanwhile)
	}
	if by := cd.DarkBy; by != c.brightBy { // (measured another way: compared from here on)
		c.bright, c.brightBy = nil, by
	}
	normal := typical(c.bright)
	dark := len(c.bright) > 0 && l.dark(normal, cd.masked())
	if !dark {
		c.darkSince++
		c.bright = append(c.bright, l)
		if len(c.bright) > noFlashRecent {
			c.bright = c.bright[1:]
		}
	}
	c.mu.Unlock()
	if dark {
		c.noFlash(n, i, l, normal, cd.masked())
	}
}

// dropNow deletes the no-flash photos kept while cooling (dropLater): to the
// Trash, off the timeline (one still coming: when it comes). c.mu held.
func (c *capture) dropNow() {
	for _, k := range c.dropLater {
		c.camera.delete(k)
	}
	c.dropLater = nil
}

// darkRunsKept is how many of the flash's last runs (photos till one came
// out dark) set how long it lasts.
const darkRunsKept = 10

// darkEvery is how many photos the flash lasts before one comes out dark:
// the average of its runs so far (each no-flash photo: another, in case one
// was a fluke), 0 if none yet. c.mu held.
func (c *capture) darkEvery() float64 {
	if len(c.darkRuns) == 0 {
		return 0
	}
	t := 0
	for _, n := range c.darkRuns {
		t += n
	}
	return float64(t) / float64(len(c.darkRuns))
}

// darkPauses is how many no-flash pauses left photos still take, the flash
// lasting every photos (0: unknown, none counted), since photos already
// since the last: the first after every-since more, then one each every.
func darkPauses(left int, every float64, since int) float64 {
	if every <= 0 {
		return 0
	}
	first := math.Max(0, math.Round(every)-float64(since))
	if float64(left) <= first {
		return 0
	}
	return math.Ceil((float64(left) - first) / math.Max(1, math.Round(every)))
}

// photoLight is a photo's brightness (its camera preview's, if it has one):
// its lit part's if masked (see litBrightness), else the whole photo's.
func photoLight(jpg []byte, masked bool) (light, error) {
	if small := jpegPreview(jpg, 400); small != nil {
		jpg = small
	}
	if masked {
		b, share, err := lumaMean(jpg, nearBlack, 4)
		return light{b, share}, err
	}
	b, err := meanBrightness(jpg)
	return light{b, 1}, err
}

// light is a photo's brightness, and the share of it lit (masked: the
// share over nearBlack; else 1, the whole photo).
type light struct{ bright, lit float64 }

// typical is the median brightness and lit share of photos.
func typical(ls []light) light {
	b, s := make([]float64, len(ls)), make([]float64, len(ls))
	for i, l := range ls {
		b[i], s[i] = l.bright, l.lit
	}
	return light{median(b), median(s)}
}

// Masked, a photo's lit part and its share both count: with no flash
// there's hardly anything lit (what light there is about: shot #4 of a
// capture, 0.17% of the frame lit, by 41 vs its flash photos' 22-31% by
// 63-79), while a flash photo's subject can be turned to fill little of the
// frame (measured over 807 flash photos of flat boards on black: down to
// 1.8% of the frame, 0.24 of the photos before; the lit part's brightness
// down to 0.57 of theirs).
const (
	litMinShare = 0.005 // a photo less of it lit than this: no flash
	litDrop     = 0.1   // nor one lit under this share of the photos before
	litDark     = 0.35  // nor one its lit part under this of theirs
)

// dark is whether a photo is a no-flash one, the photos before normal.
func (l light) dark(normal light, masked bool) bool {
	if !masked {
		return l.bright < flashDark*normal.bright
	}
	return l.lit < litMinShare || l.lit < litDrop*normal.lit || l.bright < litDark*normal.bright
}

// seedBright: a resumed capture's photos are compared with its last ones
// taken before (their files), from its first photo on.
func (c *capture) seedBright() {
	cd := c.rig.cfg.get().Cooldown
	if !cd.Dark {
		return
	}
	c.mu.Lock()
	dir := c.folder
	var files []string
	for _, s := range c.shots[:c.index] {
		if s.File != "" {
			files = append(files, s.File)
		}
	}
	c.mu.Unlock()
	var bright []light
	for _, f := range files[max(0, len(files)-noFlashRecent):] {
		if jpg, err := photoJPEG(filepath.Join(dir, f)); err == nil {
			if l, err := photoLight(jpg, cd.masked()); err == nil {
				bright = append(bright, l)
			}
		}
	}
	c.mu.Lock()
	c.bright, c.brightBy = bright, cd.DarkBy
	c.mu.Unlock()
}

// noFlash: shot i's photo n came out dark (brightness b, the ones before
// normal): the capture goes back to shot i, its photos from there to be
// deleted as it carries on (dropLater: their pictures still coming too), and
// pauses for the Cooldown's DarkMinutes (unless it's pausing already).
func (c *capture) noFlash(n, i int, l, normal light, masked bool) {
	var drop []int
	c.mu.Lock()
	if !c.running || i > c.index {
		c.mu.Unlock()
		return
	}
	for j := i; j < len(c.shots); j++ {
		s := &c.shots[j]
		s.File, s.Done, s.ActualElevation, s.ActualAzimuth = "", false, nil, nil
	}
	for k, j := range c.photoShot {
		if j >= i {
			drop = append(drop, k)
			delete(c.photoShot, k)
		}
	}
	c.index = i
	c.dropLater = append(c.dropLater, drop...)
	since, first := c.darkSince, len(c.darkRuns) == 0 // (for the log: the flash's run, this time)
	if c.darkSince > 0 {                              // (the flash lasted so many: the time left counts on it, see darkEvery)
		c.darkRuns = append(c.darkRuns, c.darkSince)
		c.darkRuns = c.darkRuns[max(0, len(c.darkRuns)-darkRunsKept):]
	}
	c.darkSince = 0
	cooling := !c.cooling.IsZero()
	num := c.shots[i].Num // (its number: the capture's, as its file's)
	if num == 0 {
		num = i + 1 // (from before Num: its place)
	}
	c.mu.Unlock()
	d := time.Duration(c.rig.cfg.get().Cooldown.DarkMinutes * float64(time.Minute))
	ran := fmt.Sprintf("%d since last pause", since)
	switch {
	case first:
		ran = fmt.Sprintf("%d since start", since)
	case since == 0:
		ran = "retake dark too"
	}
	pause := fmt.Sprintf("pausing %v", d.Round(time.Second))
	if cooling {
		pause = "already paused"
	}
	how := fmt.Sprintf("%.0f vs %.0f", l.bright, normal.bright)
	if masked {
		how = fmt.Sprintf("%.1f%% lit vs %.0f%%, by %.0f vs %.0f", 100*l.lit, 100*normal.lit, l.bright, normal.bright)
	}
	c.rig.logf("info", "capture: photo %d is dark (no flash? %s), %s; %s", num, how, ran, pause)
	if !cooling {
		c.pauseFor(d)
	}
	c.saveSoon()
	c.send()
}
