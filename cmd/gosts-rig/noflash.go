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
	if !c.rig.cfg.get().Cooldown.Dark || len(p.JPEG) == 0 {
		return
	}
	b, err := photoBrightness(p.JPEG) // (the camera's preview: much quicker)
	if err != nil {
		return
	}
	c.mu.Lock()
	if !c.running || i >= len(c.shots) || c.photoShot[n] != i {
		c.mu.Unlock()
		return // (stopped, or deleted meanwhile)
	}
	normal := median(c.bright)
	dark := len(c.bright) > 0 && b < flashDark*normal
	if !dark {
		c.darkSince++
		c.bright = append(c.bright, b)
		if len(c.bright) > noFlashRecent {
			c.bright = c.bright[1:]
		}
	}
	c.mu.Unlock()
	if dark {
		c.noFlash(n, i, b, normal)
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

// photoBrightness is a photo's brightness (its camera preview's, if it has one).
func photoBrightness(jpg []byte) (float64, error) {
	if small := jpegPreview(jpg, 400); small != nil {
		jpg = small
	}
	return meanBrightness(jpg)
}

// seedBright: a resumed capture's photos are compared with its last ones
// taken before (their files), from its first photo on.
func (c *capture) seedBright() {
	if !c.rig.cfg.get().Cooldown.Dark {
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
	var bright []float64
	for _, f := range files[max(0, len(files)-noFlashRecent):] {
		if jpg, err := photoJPEG(filepath.Join(dir, f)); err == nil {
			if b, err := photoBrightness(jpg); err == nil {
				bright = append(bright, b)
			}
		}
	}
	c.mu.Lock()
	c.bright = bright
	c.mu.Unlock()
}

// noFlash: shot i's photo n came out dark (brightness b, the ones before
// normal): the capture goes back to shot i, its photos from there to be
// deleted as it carries on (dropLater: their pictures still coming too), and
// pauses for the Cooldown's DarkMinutes (unless it's pausing already).
func (c *capture) noFlash(n, i int, b, normal float64) {
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
	c.rig.logf("info", "capture: photo %d dark (no flash? %.0f vs %.0f), %s; %s, retaking shot %d", n, b, normal, ran, pause, i+1)
	if !cooling {
		c.pauseFor(d)
	}
	c.saveSoon()
	c.send()
}
