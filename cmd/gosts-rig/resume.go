package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
		c.rig.logf("error", "capture: saving state: %v", err)
		return
	}
	if err := writeCaptureState(dir, s); err != nil {
		c.rig.logf("error", "capture: saving state: %v", err)
	}
	if s.Plan.ExportFor == "" {
		return
	}
	r := c.rig.cfg.get().Rig
	var photos []exportPhoto
	for _, sh := range s.Shots {
		if sh.File == "" || sh.ActualElevation == nil || sh.ActualAzimuth == nil || !fileExists(filepath.Join(dir, sh.File)) {
			continue // not taken (or no pose, or deleted since)
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

// recheck: an interrupted capture (opened to resume, or stopped) whose
// photos have gone since (deleted: the timeline's bin, Browse, the Finder):
// their shots aren't taken, so Resume takes them again (taken first, then
// the rest, as adopt orders them). Said when any have.
func (c *capture) recheck() {
	c.mu.Lock()
	if c.running || !c.resumable {
		c.mu.Unlock()
		return
	}
	gone := 0
	for i := range c.shots {
		if f := c.shots[i].File; f != "" && !fileExists(filepath.Join(c.folder, f)) {
			c.shots[i].File = ""
			gone++
		}
	}
	if gone == 0 {
		c.mu.Unlock()
		return
	}
	c.shots, c.index = takenFirst(c.shots)
	c.note = fmt.Sprintf("Interrupted: %d of %d photos taken. Resume takes the rest; New Capture starts afresh.", c.index, len(c.shots))
	c.mu.Unlock()
	c.rig.logf("info", "capture: %d deleted photo(s) to retake", gone)
	c.saveNow()
	c.send()
}

// adopt makes the capture in folder name the current one: its photos to
// come go into its folder. An interrupted one (see resumable): its plan is
// the plan again, and its shots are the capture's, the ones taken marked
// (Before), Resume taking the rest. Any other (done, or photos with no
// capture.json): added to (see addTo).
func (a *app) adopt(name string) error {
	if !cleanPath(name) {
		return errors.New("no such capture")
	}
	if why := a.busyWith(); why != "" {
		return errors.New(why)
	}
	dir := filepath.Join(photoDir, name)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return errors.New("no such capture")
	}
	s, err := readCaptureState(dir)
	if errors.Is(err, os.ErrNotExist) || err == nil && !s.resumable() {
		return a.addTo(name, dir)
	}
	if err != nil {
		return err
	}
	for i, sh := range s.Shots { // (a photo deleted since: its shot's not taken, to be taken again)
		if sh.File != "" && !fileExists(filepath.Join(dir, sh.File)) {
			s.Shots[i].File = ""
		}
	}
	for i := range s.Shots { // (from before Num: the order they're in)
		if s.Shots[i].Num == 0 {
			s.Shots[i].Num = i + 1
		}
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
	c.rig.logf("info", "capture: %s opened (%d/%d taken)", name, s.taken(), len(shots))
	a.rig.sendState() // (the plan: back)
	c.send()
	return nil
}

// addTo makes folder dir (name: its path) the current capture, to add
// photos to: its photos on the timeline, the ones to come going into it; a
// capture there keeps the shots taken before (see priorShots), its own
// numbered on from them. The plan: as it is.
func (a *app) addTo(name, dir string) error {
	ps, err := batchPhotos(dir)
	if err != nil {
		return err
	}
	files := make([]string, len(ps))
	for i, p := range ps {
		files[i] = p.File
	}
	prior, _ := priorShots(dir)
	c := a.cap
	a.camera.resumeIn(dir, files)
	c.mu.Lock()
	c.shots, c.index, c.folder, c.complete, c.resumable = prior, len(prior), dir, len(prior) > 0, false
	c.photoShot = map[int]int{}
	c.note = fmt.Sprintf("Adding to %s (%d photos in it): a capture's photos go in with them, numbered on.", filepath.Base(dir), len(files))
	c.mu.Unlock()
	c.rig.logf("info", "capture: %s opened to add to (%d photos)", name, len(files))
	c.send()
	return nil
}

// priorShots: the shots taken before in folder dir (capture.json's, their
// photo still there), marked so (Done, Before), and the highest capt_
// number there (a capture's own numbered on from it: none overwritten).
func priorShots(dir string) ([]shot, int) {
	top := 0
	var out []shot
	if s, err := readCaptureState(dir); err == nil {
		for _, sh := range s.Shots {
			if sh.File == "" || !fileExists(filepath.Join(dir, sh.File)) {
				continue
			}
			sh.Done, sh.Before = true, true
			out = append(out, sh)
			top = max(top, sh.Num)
		}
	}
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			n := strings.ToLower(e.Name())
			if v, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(n, "capt_"), filepath.Ext(n))); err == nil && strings.HasPrefix(n, "capt_") {
				top = max(top, v)
			}
		}
	}
	return out, top
}

// fileStem is the name (no extension) photo n's files get if it's one of
// the capture's shots: capt_ and its place in the plan (capt_001: as many
// digits as the plan needs, 3 at least), so the folder's in the plan's
// order by name, whenever each was taken (a resumed capture takes some
// shots late). "" for a photo that isn't (Take Photo: the camera's name).
func (c *capture) fileStem(n int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	i, ok := c.photoShot[n]
	if !ok || i >= len(c.shots) || c.shots[i].Num == 0 {
		return ""
	}
	return fmt.Sprintf("capt_%0*d", max(3, len(strconv.Itoa(len(c.shots)))), c.shots[i].Num)
}

// shotNum is the number photo n shows if it's one of the capture's shots:
// its place in the plan, as its file's name (capt_001: 1); 0 if it isn't.
func (c *capture) shotNum(n int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i, ok := c.photoShot[n]; ok && i < len(c.shots) {
		return c.shots[i].Num
	}
	return 0
}

// renameStem renames files (a photo's: its JPEG, its RAW) to stem, each
// keeping its extension (lower case); one there already by that name (the
// shot's photo before, taken again) goes to the Trash first. The files as
// they are now.
func renameStem(files []string, stem string) []string {
	out := make([]string, len(files))
	for i, f := range files {
		to := filepath.Join(filepath.Dir(f), stem+strings.ToLower(filepath.Ext(f)))
		out[i] = f
		if to == f {
			continue
		}
		if fileExists(to) {
			trash(to)
		}
		if err := os.Rename(f, to); err == nil {
			out[i] = to
		}
	}
	return out
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
