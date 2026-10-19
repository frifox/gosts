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
	"syscall"
	"time"
)

// Past captures on disk: the batch folders in photoDir (see batchDir), each
// a capture's photos, for the page's Open Capture. Sample shots' folders
// ("-samples") aren't captures: left out.

// batchInfo is a batch folder: its name (when it started), how many photos,
// the middle one's file (its thumbnail stands for the folder), and whether
// it's the batch being taken now.
type batchInfo struct {
	Name    string `json:"name"`              // its path in photoDir ("drones/cinelog 1": in a folder)
	Title   string `json:"title"`             // its own name (the path's last)
	Folders int    `json:"folders,omitempty"` // folders in it (see folderKids)
	Count   int    `json:"count"`
	Models  int    `json:"models,omitempty"` // models in it (see batchModels)
	Works   int    `json:"works,omitempty"`  // Combine 3D work files in it (see worksIn)
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
	// By name: a capture's photos are named after their place in its plan
	// (capt_001.jpg: see capture.fileStem), so that's their order, whenever
	// each was taken (a resumed capture takes some late).
	slices.SortFunc(out, func(a, b batchPhoto) int { return strings.Compare(a.File, b.File) })
	return out, nil
}

// Folders: photoDir holds folders, at any depth: a capture's (its photos,
// models, work files) or one to keep others in, or both. Each is known by
// its path in photoDir ("drones/cinelog 1"); a model's own folder (in its
// capture's), a build's, a capture's samples: not folders of these.

// folderKids are folder rel's folders (not its models', nor a build's or
// samples'), by name.
func folderKids(rel string) []string {
	dir := filepath.Join(photoDir, filepath.FromSlash(rel))
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() || strings.HasPrefix(n, ".") || strings.HasSuffix(n, "-samples") || n == buildDir || n == "samples" || n == "colmap" { // (a capture's own: its build, samples, alignment data)
			continue
		}
		if _, ok := modelIn(filepath.Join(dir, n)); ok {
			continue // (a model's folder)
		}
		out = append(out, n)
	}
	return out
}

// relPath is dir's path in photoDir ("" if it isn't in it).
func relPath(dir string) string {
	r, err := filepath.Rel(photoDir, dir)
	if err != nil || r == "." || strings.HasPrefix(r, "..") {
		return ""
	}
	return filepath.ToSlash(r)
}

// listFolder lists folder rel's folders ("": photoDir's), newest first (a
// capture's name is its start time); the current capture's shown even with
// nothing in it yet.
func listFolder(rel string) ([]batchInfo, error) {
	if _, err := os.Stat(filepath.Join(photoDir, filepath.FromSlash(rel))); os.IsNotExist(err) {
		return []batchInfo{}, nil
	}
	batch.Lock()
	current := relPath(batch.dir)
	batch.Unlock()
	out := []batchInfo{}
	for _, n := range folderKids(rel) {
		p := n
		if rel != "" {
			p = rel + "/" + n
		}
		out = append(out, folderInfo(p, current)) // (empty ones too: there to delete, or fill)
	}
	slices.SortFunc(out, func(a, b batchInfo) int { return strings.Compare(b.Title, a.Title) })
	return out, nil
}

// folderMark marks a folder made in Browse (New Folder).
const folderMark = ".gosts-folder"

// folderInfo: folder p, as Browse shows it (current: the current capture's
// path).
func folderInfo(p, current string) batchInfo {
	dir := filepath.Join(photoDir, filepath.FromSlash(p))
	ps, _ := batchPhotos(dir)
	ms, _ := batchModels(dir)
	mid := ""
	if len(ps) > 0 {
		mid = ps[(len(ps)-1)/2].File
	}
	folders := len(folderKids(p))
	bi := batchInfo{Name: p, Title: filepath.Base(dir), Count: len(ps), Models: len(ms), Works: len(worksIn(p)), Folders: folders, Mid: mid, Current: p == current}
	if s, err := readCaptureState(dir); err == nil && s.resumable() {
		bi.Interrupted, bi.Taken, bi.Planned = true, s.taken(), len(s.Shots)
	}
	return bi
}

// allFolders lists every folder in photoDir, at any depth (by path).
func allFolders() []batchInfo {
	batch.Lock()
	current := relPath(batch.dir)
	batch.Unlock()
	out := []batchInfo{}
	var walk func(rel string)
	walk = func(rel string) {
		for _, n := range folderKids(rel) {
			p := n
			if rel != "" {
				p = rel + "/" + n
			}
			out = append(out, folderInfo(p, current))
			walk(p)
		}
	}
	walk("")
	slices.SortFunc(out, func(a, b batchInfo) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// cleanName is a folder or file name from a request: one name, nothing that
// climbs out of photoDir.
func cleanName(n string) bool {
	return n != "" && n == filepath.Base(n) && !strings.HasPrefix(n, ".") && !strings.ContainsAny(n, `/\`)
}

// cleanPath is a folder's path in photoDir from a request: names (cleanName
// each), slash between; none a samples' folder.
func cleanPath(p string) bool {
	if p == "" {
		return false
	}
	for _, n := range strings.Split(p, "/") {
		if !cleanName(n) || strings.HasSuffix(n, "-samples") {
			return false
		}
	}
	return true
}

// handleBatches lists folders (GET /batches): photoDir's; ?in=: a
// folder's; ?all=1: all of them, at any depth.
func (a *app) handleBatches(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("all") == "1" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(allFolders())
		return
	}
	in := q.Get("in")
	if in != "" && !cleanPath(in) {
		http.Error(w, "bad folder", http.StatusBadRequest)
		return
	}
	bs, err := listFolder(in)
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
	if !cleanPath(name) {
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
	if !cleanPath(name) || !cleanName(file) {
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

// batchThumb is a photo's thumbnail, from the cache (by the file itself,
// not its path: see fileID, so a folder renamed keeps its thumbnails) or
// made now and kept there.
func batchThumb(path string, info os.FileInfo) ([]byte, error) {
	cache := thumbCache(fmt.Sprintf("%s|%d", fileID(path, info), batchThumbWidth),
		fmt.Sprintf("%s|%d|%d|%d", path, info.Size(), info.ModTime().UnixNano(), batchThumbWidth))
	if cache != "" {
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

// fileID names a file whatever its path (its folder renamed, or it: the
// same): its device and inode, with its size and time (changed: another);
// its path, size and time where there's no inode.
func fileID(path string, info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("ino|%d|%d|%d|%d", st.Dev, st.Ino, info.Size(), info.ModTime().UnixNano())
	}
	return fmt.Sprintf("%s|%d|%d", path, info.Size(), info.ModTime().UnixNano())
}

// thumbCache is where a picture keyed key is kept (the cache folder's
// thumbs; "" if there's none). One kept under old (as they were keyed
// before: by path) is moved there, not made again.
func thumbCache(key, old string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	at := func(k string) string {
		sum := sha1.Sum([]byte(k))
		return filepath.Join(dir, "gosts-rig", "thumbs", hex.EncodeToString(sum[:])+".jpg")
	}
	p := at(key)
	if old != "" && !fileExists(p) {
		os.Rename(at(old), p) // (none: nothing)
	}
	return p
}

// renameBatch renames folder from (a path) to the name to, where it is.
func (a *app) renameBatch(from, to string) error {
	to = strings.TrimSpace(to)
	if !cleanName(to) || strings.HasSuffix(to, "-samples") || strings.ContainsAny(to, `:*?"<>|`) {
		return errors.New("not a folder name: no slashes, no leading dot, nor any of : * ? \" < > |, nor ending in -samples")
	}
	dst := to
	if i := strings.LastIndexByte(from, '/'); i >= 0 {
		dst = from[:i+1] + to
	}
	return a.moveBatch(from, dst)
}

// moveBatch moves folder from (a path in photoDir) to path to: renamed, or
// into another folder (made if need be), and its samples' folder with it.
// The current capture in it keeps going there; not while a capture runs
// into it, nor a model's built in it. Combine 3D's work files naming its
// models: told where they are now.
func (a *app) moveBatch(from, to string) error {
	if !cleanPath(from) {
		return errors.New("no such folder")
	}
	if !cleanPath(to) {
		return errors.New("not a place for it: folder names, no leading dots")
	}
	if to == from {
		return nil
	}
	if strings.HasPrefix(to+"/", from+"/") {
		return errors.New("a folder can't go into itself")
	}
	src, dst := filepath.Join(photoDir, from), filepath.Join(photoDir, to)
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("there's a folder named %q there already", filepath.Base(dst))
	}
	if why := a.model.buildingIn(from); why != "" {
		return errors.New(why)
	}
	sep := string(filepath.Separator)
	batch.Lock()
	in := batch.dir == src || strings.HasPrefix(batch.dir, src+sep) // (the current capture: it, or in it)
	if in {
		if why := a.currentBusy(); why != "" {
			batch.Unlock()
			return errors.New(why)
		}
	}
	if _, err := os.Stat(src); err != nil {
		if batch.dir != src {
			batch.Unlock()
			return errors.New("no such folder")
		}
		// The current capture with nothing saved yet (its folder made with
		// its first photo): only its name changes.
		batch.dir = dst
		batch.Unlock()
		a.broadcastBatch()
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		batch.Unlock()
		return err
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
	if in {
		oldDir := batch.dir
		batch.dir = dst + strings.TrimPrefix(oldDir, src)
		if a.camera != nil {
			a.camera.relocate(oldDir, batch.dir) // (the timeline's files: there now)
			a.cap.relocate(oldDir, batch.dir)
		}
	}
	batch.Unlock()
	if in {
		a.broadcastBatch() // (after the lock: it reads the name)
	}
	rewriteWorkRefs(from, to)
	log.Printf("photos: %s moved to %s", from, to)
	return nil
}

// handleBatchMove moves a folder (POST /batches/{name}/move, {"to": …}: the
// folder it goes into, "" for photoDir itself), its name kept.
func (a *app) handleBatchMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To string `json:"to"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	from := r.PathValue("name")
	to := filepath.Base(filepath.FromSlash(from))
	if req.To != "" {
		to = req.To + "/" + to
	}
	if err := a.moveBatch(from, to); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"name": to})
}

// handleBatchNew makes a folder (POST /batches/new, {"in": its folder, "": photoDir; "name": …}).
func (a *app) handleBatchNew(w http.ResponseWriter, r *http.Request) {
	var req struct {
		In   string `json:"in"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if !cleanName(name) || strings.HasSuffix(name, "-samples") || strings.ContainsAny(name, `:*?"<>|`) {
		http.Error(w, "not a folder name: no slashes, no leading dot, nor any of : * ? \" < > |", http.StatusBadRequest)
		return
	}
	p := name
	if req.In != "" {
		if !cleanPath(req.In) {
			http.Error(w, "bad folder", http.StatusBadRequest)
			return
		}
		p = req.In + "/" + name
	}
	dir := filepath.Join(photoDir, filepath.FromSlash(p))
	if _, err := os.Stat(dir); err == nil {
		http.Error(w, fmt.Sprintf("there's a folder named %q there already", name), http.StatusConflict)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	os.WriteFile(filepath.Join(dir, folderMark), nil, 0o644) // (listed even empty)
	log.Printf("photos: folder %s made", p)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"name": p})
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
	if !cleanPath(name) {
		return errors.New("no such capture")
	}
	dir := filepath.Join(photoDir, name)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return errors.New("no such capture")
	}
	batch.Lock()
	current := batch.dir == dir || strings.HasPrefix(batch.dir, dir+string(filepath.Separator)) // (it, or in it)
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
	if !cleanPath(name) || !cleanName(file) {
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
