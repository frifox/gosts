package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Resuming: a capture keeps its plan and its shots, taken or not, in its
// folder (captureFile), saved as each photo comes over (with its alignment
// data, rewritten then too), so one that stopped short (Stop, an error, the
// app closed or crashed) can carry on: opened in Browse, it's the current
// capture again, its plan back, and Resume takes the shots left (the object
// stays put: the rig's angles are the same from one run to the next).

// captureFile is a capture's state, in its folder.
const captureFile = "capture.json"

type captureState struct {
	Plan     Plan      `json:"plan"`
	Shots    []shot    `json:"shots"`
	Complete bool      `json:"complete"` // every shot taken: nothing to resume
	Started  time.Time `json:"started"`
	Updated  time.Time `json:"updated"`
}

// taken is how many of the shots were taken (their photo's file saved).
func (s captureState) taken() int {
	n := 0
	for _, sh := range s.Shots {
		if sh.File != "" {
			n++
		}
	}
	return n
}

func (s captureState) resumable() bool { return !s.Complete && len(s.Shots) > 0 }

// readCaptureState reads folder dir's capture state (os.ErrNotExist: none).
func readCaptureState(dir string) (captureState, error) {
	var s captureState
	b, err := os.ReadFile(filepath.Join(dir, captureFile))
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s: %w", captureFile, err)
	}
	return s, nil
}

func writeCaptureState(dir string, s captureState) error {
	s.Updated = time.Now()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".capture-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, captureFile))
}

// stateNow is the capture's state as it is (to save).
func (c *capture) stateNow() (string, captureState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.folder, captureState{Plan: c.plan, Shots: append([]shot(nil), c.shots...), Complete: c.complete, Started: c.began}
}

// saveSoon saves the capture's state and alignment data shortly (photos
// coming over one after another: written about once a second, not each).
func (c *capture) saveSoon() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.saveTimer != nil || c.folder == "" {
		return
	}
	c.saveTimer = time.AfterFunc(time.Second, func() {
		c.mu.Lock()
		c.saveTimer = nil
		c.mu.Unlock()
		c.saveNow()
	})
}

// saveNow writes the capture's state, and its alignment data for the photos
// taken so far (the plan's app: see writeExport), in its folder.
func (c *capture) saveNow() {
	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	dir, s := c.stateNow()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.rig.logf("error", "capture: saving its state: %v", err)
		return
	}
	if err := writeCaptureState(dir, s); err != nil {
		c.rig.logf("error", "capture: saving its state: %v", err)
	}
	if s.Plan.ExportFor == "" {
		return
	}
	r := c.rig.cfg.get().Rig
	var photos []exportPhoto
	for _, sh := range s.Shots {
		if sh.File == "" || sh.ActualElevation == nil || sh.ActualAzimuth == nil {
			continue // not taken (or no pose)
		}
		photos = append(photos, exportPhoto{Path: filepath.Join(dir, sh.File), Pose: camPose(r, *sh.ActualElevation, *sh.ActualAzimuth)})
	}
	if len(photos) == 0 {
		return
	}
	c.exporting.Add(1) // (the folder left as it is meanwhile: see renameBatch)
	defer c.exporting.Add(-1)
	if err := writeExport(s.Plan.ExportFor, dir, photos, exportIntrinsics{FocalMM: c.camera.focalLength()}); err != nil {
		c.rig.logf("error", "capture: alignment data (%s): %v", s.Plan.ExportFor, err)
	}
}

// pictured: photo n's picture came over (its file saved): if it's one of
// the capture's shots, that's recorded (and saved soon).
func (c *capture) pictured(n int, p photo) {
	c.mu.Lock()
	i, ok := c.photoShot[n]
	if ok && i < len(c.shots) && len(p.Files) > 0 && filepath.Dir(p.Files[0]) == c.folder {
		c.shots[i].File = filepath.Base(p.Files[0])
	} else {
		ok = false
	}
	c.mu.Unlock()
	if ok {
		c.saveSoon()
		go c.checkFlash(n, i, p)
	}
}

// adopt makes the capture in folder name (an interrupted one: see
// resumable) the current one: its photos to come go into its folder, its
// plan is the plan again, and its shots are the capture's, the ones taken
// marked (Before), Resume taking the rest.
func (a *app) adopt(name string) error {
	if !cleanName(name) {
		return errors.New("no such capture")
	}
	if why := a.busyWith(); why != "" {
		return errors.New(why)
	}
	dir := filepath.Join(photoDir, name)
	s, err := readCaptureState(dir)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("that capture has nothing to resume (no capture.json)")
	}
	if err != nil {
		return err
	}
	if !s.resumable() {
		return errors.New("that capture is complete")
	}
	c := a.cap
	shots, index := takenFirst(s.Shots) // (taken: their file saved; Resume carries on from the first not)
	if err := a.cfg.update(func(cf *Config) { cf.Plan = s.Plan }); err != nil {
		return err
	}
	var files []string
	for _, sh := range shots[:index] {
		files = append(files, sh.File)
	}
	a.camera.resumeIn(dir, files) // (its photos on the timeline again)
	c.mu.Lock()
	c.shots, c.index, c.plan, c.folder, c.complete, c.resumable = shots, index, s.Plan, dir, false, true
	c.began, c.photoShot = s.Started, map[int]int{}
	c.note = fmt.Sprintf("Interrupted: %d of %d photos taken. Resume takes the rest; New Capture starts afresh.", s.taken(), len(shots))
	c.mu.Unlock()
	c.rig.logf("info", "capture: %s opened to resume (%d of %d photos taken)", name, s.taken(), len(shots))
	a.rig.sendState() // (the plan: back)
	c.send()
	return nil
}

// relocate: the capture's folder was renamed (from to to).
func (c *capture) relocate(from, to string) {
	c.mu.Lock()
	if c.folder == from {
		c.folder = to
	}
	c.mu.Unlock()
}

// forget: the capture is no longer resumable (New Capture: a fresh one).
func (c *capture) forget() {
	c.mu.Lock()
	c.resumable, c.folder, c.photoShot = false, "", nil
	c.mu.Unlock()
	c.preview(c.rig.cfg.get().Plan)
}
