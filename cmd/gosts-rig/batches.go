package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Past captures on disk: the batch folders in photoDir (see batchDir), each
// a capture's photos, for the page's Open Capture. Sample shots' folders
// ("-samples") aren't captures: left out.

// batchInfo is a batch folder: its name (when it started), how many photos,
// the middle one's file (its thumbnail stands for the folder), and whether
// it's the batch being taken now.
type batchInfo struct {
	Name    string `json:"name"`
	Count   int    `json:"count"`
	Mid     string `json:"mid"`
	Current bool   `json:"current"`
	// Interrupted: a capture that stopped short (see resume.go): taken of
	// planned photos; opened, it can be resumed.
	Interrupted bool `json:"interrupted,omitempty"`
	Taken       int  `json:"taken,omitempty"`
	Planned     int  `json:"planned,omitempty"`
}

// batchPhoto is a photo in a batch folder.
type batchPhoto struct {
	File string `json:"file"`
	Size int64  `json:"size"`
	At   int64  `json:"at"` // modified, ms since 1970
}

// rawExts are the RAW files a photo may be only (no JPEG beside it): shown
// by the preview inside (see rawPreview).
var rawExts = []string{".arw", ".nef", ".cr2", ".cr3", ".dng", ".raf", ".orf", ".rw2"}

func isJPEG(name string) bool {
	e := strings.ToLower(filepath.Ext(name))
	return e == ".jpg" || e == ".jpeg"
}

// batchPhotos are a folder's photos by file name: its JPEGs, and RAWs with
// no JPEG of the same name (a RAW-only photo).
func batchPhotos(dir string) ([]batchPhoto, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	jpgs := map[string]bool{}
	for _, e := range ents {
		if !e.IsDir() && isJPEG(e.Name()) {
			jpgs[strings.ToLower(strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))] = true
		}
	}
	var out []batchPhoto
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, ".") {
			continue
		}
		raw := slices.Contains(rawExts, strings.ToLower(filepath.Ext(n)))
		if !isJPEG(n) && !(raw && !jpgs[strings.ToLower(strings.TrimSuffix(n, filepath.Ext(n)))]) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, batchPhoto{File: n, Size: info.Size(), At: info.ModTime().UnixMilli()})
	}
	slices.SortFunc(out, func(a, b batchPhoto) int { return strings.Compare(a.File, b.File) })
	return out, nil
}

// listBatches are the batch folders with photos, newest first (their names
// are their start times).
func listBatches() ([]batchInfo, error) {
	ents, err := os.ReadDir(photoDir)
	if os.IsNotExist(err) {
		return []batchInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	batch.Lock()
	current := filepath.Base(batch.dir)
	batch.Unlock()
	out := []batchInfo{}
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() || strings.HasPrefix(n, ".") || strings.HasSuffix(n, "-samples") {
			continue
		}
		ps, err := batchPhotos(filepath.Join(photoDir, n))
		if err != nil || (len(ps) == 0 && n != current) { // (the current one: shown empty too)
			continue
		}
		mid := ""
		if len(ps) > 0 {
			mid = ps[(len(ps)-1)/2].File
		}
		bi := batchInfo{Name: n, Count: len(ps), Mid: mid, Current: n == current}
		if s, err := readCaptureState(filepath.Join(photoDir, n)); err == nil && s.resumable() {
			bi.Interrupted, bi.Taken, bi.Planned = true, s.taken(), len(s.Shots)
		}
		out = append(out, bi)
	}
	slices.SortFunc(out, func(a, b batchInfo) int { return strings.Compare(b.Name, a.Name) })
	return out, nil
}

// cleanName is a folder or file name from a request: one name, nothing that
// climbs out of photoDir.
func cleanName(n string) bool {
	return n != "" && n == filepath.Base(n) && !strings.HasPrefix(n, ".") && !strings.ContainsAny(n, `/\`)
}

// handleBatches lists the batch folders (GET /batches).
func (a *app) handleBatches(w http.ResponseWriter, r *http.Request) {
	bs, err := listBatches()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(bs)
}

// handleBatch lists a batch folder's photos (GET /batches/{name}).
func (a *app) handleBatch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !cleanName(name) || strings.HasSuffix(name, "-samples") {
		http.NotFound(w, r)
		return
	}
	ps, err := batchPhotos(filepath.Join(photoDir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if ps == nil {
		ps = []batchPhoto{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ps)
}

// handleBatchPhoto serves a batch folder's photo (GET /batches/{name}/{file}):
// the JPEG (a RAW's preview), or with ?thumb a thumbnail (made once, kept in
// the user's cache folder).
func (a *app) handleBatchPhoto(w http.ResponseWriter, r *http.Request) {
	name, file := r.PathValue("name"), r.PathValue("file")
	if !cleanName(name) || !cleanName(file) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(photoDir, name, file)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if !r.URL.Query().Has("thumb") {
		if isJPEG(file) {
			http.ServeFile(w, r, path)
			return
		}
		jpg, err := photoJPEG(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(jpg)
		return
	}
	jpg, err := batchThumb(path, info)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Write(jpg)
}

// photoJPEG is a photo file as a JPEG: itself, or a RAW's preview.
func photoJPEG(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if isJPEG(path) {
		return data, nil
	}
	return rawPreview(data)
}

// batchThumbWidth is how wide (at least) a batch's thumbnails are: grid
// tiles up to ~440 px wide on a 2× screen.
const batchThumbWidth = 600

// thumbSlots limits how many thumbnails are made at once (each decodes a
// whole photo): a folder of hundreds asked for together doesn't swamp the
// machine.
var thumbSlots = make(chan struct{}, 3)

// batchThumb is a photo's thumbnail, from the cache (by the file's path,
// size and time) or made now and kept there.
func batchThumb(path string, info os.FileInfo) ([]byte, error) {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d|%d", path, info.Size(), info.ModTime().UnixNano(), batchThumbWidth)))
	cache := ""
	if dir, err := os.UserCacheDir(); err == nil {
		cache = filepath.Join(dir, "gosts-rig", "thumbs", hex.EncodeToString(sum[:])+".jpg")
		if data, err := os.ReadFile(cache); err == nil {
			return data, nil
		}
	}
	thumbSlots <- struct{}{}
	defer func() { <-thumbSlots }()
	jpg, err := photoJPEG(path)
	if err != nil {
		return nil, err
	}
	t, err := thumbnail(jpg, batchThumbWidth)
	if err != nil {
		return nil, err
	}
	if cache != "" && os.MkdirAll(filepath.Dir(cache), 0o755) == nil {
		os.WriteFile(cache, t, 0o644)
	}
	return t, nil
}

// renameBatch renames a batch folder (and its samples' folder with it). The
// batch being taken keeps going in the new folder; not while a capture runs
// into it.
func (a *app) renameBatch(from, to string) error {
	to = strings.TrimSpace(to)
	if !cleanName(from) || strings.HasSuffix(from, "-samples") {
		return errors.New("no such capture")
	}
	if !cleanName(to) || strings.HasSuffix(to, "-samples") || strings.ContainsAny(to, `:*?"<>|`) {
		return errors.New("not a folder name: no slashes, no leading dot, nor any of : * ? \" < > |, nor ending in -samples")
	}
	if to == from {
		return nil
	}
	src, dst := filepath.Join(photoDir, from), filepath.Join(photoDir, to)
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("there's a folder named %q already", to)
	}
	batch.Lock()
	current := batch.dir == src
	if current {
		if why := a.currentBusy(); why != "" {
			batch.Unlock()
			return errors.New(why)
		}
	}
	if _, err := os.Stat(src); err != nil {
		if !current {
			batch.Unlock()
			return errors.New("no such capture")
		}
		// The current capture with nothing saved yet (its folder made with
		// its first photo): only its name changes.
		batch.dir = dst
		batch.Unlock()
		a.broadcastBatch()
		return nil
	}
	if err := os.Rename(src, dst); err != nil {
		batch.Unlock()
		return err
	}
	if _, err := os.Stat(src + "-samples"); err == nil {
		if _, err := os.Stat(dst + "-samples"); os.IsNotExist(err) {
			os.Rename(src+"-samples", dst+"-samples")
		}
	}
	if current {
		batch.dir = dst
		if a.camera != nil {
			a.camera.relocate(src, dst) // (the timeline's files: there now)
			a.cap.relocate(src, dst)
		}
	}
	batch.Unlock()
	if current {
		a.broadcastBatch() // (after the lock: it reads the name)
	}
	log.Printf("photos: renamed %s to %s", from, to)
	return nil
}

// handleBatchRename renames a batch folder (POST /batches/{name}/rename,
// {"to": "new name"}).
func (a *app) handleBatchRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To string `json:"to"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := a.renameBatch(r.PathValue("name"), req.To); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Deleting: a photo or a capture goes to the Trash (~/.Trash), not gone for
// good: a click by mistake can be undone in the Finder.

// trash moves path into the user's Trash, under a name not taken there.
func trash(path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".Trash")
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		ext = ""
	}
	stem := strings.TrimSuffix(base, ext)
	// A free name: as it is, else "name 2", "name 3"… If the Trash can't be
	// looked into (macOS may not let us), a name with the time in it, so
	// nothing there is replaced.
	dst := filepath.Join(dir, base)
	for i := 2; ; i++ {
		_, err := os.Lstat(dst)
		if os.IsNotExist(err) {
			break
		}
		if err != nil || i > 999 {
			dst = filepath.Join(dir, fmt.Sprintf("%s %s%s", stem, time.Now().Format("2006-01-02 15.04.05.000000000"), ext))
			break
		}
		dst = filepath.Join(dir, fmt.Sprintf("%s %d%s", stem, i, ext))
	}
	return os.Rename(path, dst)
}

// trashPhoto trashes a photo's file and its siblings: the files beside it
// of the same name (its RAW, say).
func trashPhoto(path string) error {
	dir, base := filepath.Dir(path), filepath.Base(path)
	stem := strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base)))
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() && strings.ToLower(strings.TrimSuffix(n, filepath.Ext(n))) == stem {
			if err := trash(filepath.Join(dir, n)); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// deleteBatch trashes a capture's folder (and its samples' folder). The
// batch being taken: not while a capture runs; else the timeline's cleared
// and the next photos start a new folder (as New Capture).
func (a *app) deleteBatch(name string) error {
	if !cleanName(name) || strings.HasSuffix(name, "-samples") {
		return errors.New("no such capture")
	}
	dir := filepath.Join(photoDir, name)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return errors.New("no such capture")
	}
	batch.Lock()
	current := batch.dir == dir
	batch.Unlock()
	if current {
		if why := a.currentBusy(); why != "" {
			return errors.New(why)
		}
		a.camera.reset(false)
		a.cap.forget()
	}
	if err := trash(dir); err != nil {
		return err
	}
	if _, err := os.Stat(dir + "-samples"); err == nil {
		trash(dir + "-samples")
	}
	log.Printf("photos: %s moved to the Trash", name)
	return nil
}

// broadcastBatch tells the pages the current capture's name (renamed).
func (a *app) broadcastBatch() {
	if a.camera != nil {
		a.broadcast(a.camera.batchStatus())
	} else {
		a.broadcast(batchNow())
	}
}

// currentBusy says why the current capture's folder can't be renamed or
// deleted just now: a capture saving into it, a photo still coming over
// from the camera (it'd land in the old folder), or the alignment data
// still being written after a capture; "" if it can.
func (a *app) currentBusy() string {
	switch {
	case a.cap != nil && a.cap.isRunning():
		return "a capture is saving into it: try once the capture is done"
	case a.camera != nil && a.camera.backlog() > 0:
		return "a photo is still coming over from the camera: try again in a moment"
	case a.cap != nil && a.cap.exporting.Load() > 0:
		return "the capture's alignment data is still being written: try again in a moment"
	}
	return ""
}

// handleBatchDelete trashes a capture (POST /batches/{name}/delete).
func (a *app) handleBatchDelete(w http.ResponseWriter, r *http.Request) {
	if err := a.deleteBatch(r.PathValue("name")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleBatchPhotoDelete trashes a capture's photo (POST
// /batches/{name}/{file}/delete), its RAW too.
func (a *app) handleBatchPhotoDelete(w http.ResponseWriter, r *http.Request) {
	name, file := r.PathValue("name"), r.PathValue("file")
	if !cleanName(name) || !cleanName(file) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(photoDir, name, file)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	if err := trashPhoto(path); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.camera.forgetFile(path) // (off the timeline too, if it's on it)
	w.WriteHeader(http.StatusNoContent)
}
