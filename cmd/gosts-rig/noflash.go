package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Auto-pause after a no-flash photo (Cooldown.Dark): each of a capture's
// photos is looked at as it comes over, and one much darker than the ones
// before it (under flashDark of theirs: the flash didn't fire, overheated)
// pauses the capture for DarkMinutes; then it carries on from that photo's
// shot. The photos from it on (the dark one, and any taken after it before
// the pause took hold) are set aside in the capture's noFlashDir, and their
// shots taken again.

// noFlashDir is where a capture's photos set aside go (in its folder): out
// of its photos (Browse, Build Model and the photogrammetry apps see only
// the folder's own), kept.
const noFlashDir = "no-flash"

// noFlashRecent is how many of the last bright photos set the brightness a
// photo is compared with (their median).
const noFlashRecent = 10

// checkFlash looks at shot i's photo n (just come over, its file saved) for
// a flash that didn't fire. (In the background: it's decoded.)
func (c *capture) checkFlash(n, i int, p photo) {
	if !c.rig.cfg.get().Cooldown.Dark || len(p.JPEG) == 0 {
		return
	}
	jpg := p.JPEG
	if small := jpegPreview(jpg, 400); small != nil { // (the camera's preview: much quicker)
		jpg = small
	}
	b, err := meanBrightness(jpg)
	if err != nil {
		return
	}
	c.mu.Lock()
	if !c.running || i >= len(c.shots) || c.photoShot[n] != i {
		c.mu.Unlock()
		return // (stopped, or set aside meanwhile)
	}
	normal := median(c.bright)
	dark := len(c.bright) >= 3 && b < flashDark*normal
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

// noFlash: shot i's photo n came out dark (brightness b, the ones before
// normal): the capture goes back to shot i, its photos from there set aside,
// and pauses for the Cooldown's DarkMinutes (unless it's pausing already).
func (c *capture) noFlash(n, i int, b, normal float64) {
	var aside []string
	c.mu.Lock()
	if !c.running || i > c.index {
		c.mu.Unlock()
		return
	}
	for j := i; j < len(c.shots); j++ {
		s := &c.shots[j]
		if s.File != "" {
			aside = append(aside, s.File)
		}
		s.File, s.Done, s.ActualElevation, s.ActualAzimuth = "", false, nil, nil
	}
	if c.aside == nil {
		c.aside = map[int]bool{}
	}
	for k, j := range c.photoShot { // (their pictures still coming: set aside as they come)
		if j >= i {
			c.aside[k] = true
			delete(c.photoShot, k)
		}
	}
	c.index = i
	dir := c.folder
	cooling := !c.cooling.IsZero()
	c.mu.Unlock()
	for _, f := range aside {
		setAside(dir, f)
	}
	d := time.Duration(c.rig.cfg.get().Cooldown.DarkMinutes * float64(time.Minute))
	c.rig.logf("info", "capture: photo %d came out dark (brightness %.0f, the ones before %.0f: the flash didn't fire?): set aside, with any after it; %s, then on from its shot (%d)",
		n, b, normal, map[bool]string{true: "pausing already", false: fmt.Sprintf("pausing %v to let the flash cool", d.Round(time.Second))}[cooling], i+1)
	if !cooling {
		c.pauseFor(d)
	}
	c.saveSoon()
	c.send()
}

// setAside moves file (in the capture's folder dir) into its noFlashDir.
func setAside(dir, file string) {
	to := filepath.Join(dir, noFlashDir)
	if err := os.MkdirAll(to, 0o755); err != nil {
		return
	}
	os.Rename(filepath.Join(dir, file), filepath.Join(to, file))
	xmp := strings.TrimSuffix(file, filepath.Ext(file)) + ".xmp" // (its RealityScan pose, if written)
	if _, err := os.Stat(filepath.Join(dir, xmp)); err == nil {
		os.Rename(filepath.Join(dir, xmp), filepath.Join(to, xmp))
	}
}
