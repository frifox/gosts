package main

import (
	"fmt"
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
	cooling := !c.cooling.IsZero()
	c.mu.Unlock()
	d := time.Duration(c.rig.cfg.get().Cooldown.DarkMinutes * float64(time.Minute))
	c.rig.logf("info", "capture: photo %d came out dark (brightness %.0f, the ones before %.0f: the flash didn't fire?): it (and %d after it) deleted as it carries on; %s, then it's taken again (shot %d)",
		n, b, normal, len(drop)-1, map[bool]string{true: "pausing already", false: fmt.Sprintf("pausing %v to let the flash cool", d.Round(time.Second))}[cooling], i+1)
	if !cooling {
		c.pauseFor(d)
	}
	c.saveSoon()
	c.send()
}
