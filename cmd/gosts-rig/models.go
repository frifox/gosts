package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// A capture's models: its folders with a model in them (an OBJ, or a USDZ),
// Build Model's or put there by hand. The gallery shows them first, as
// tiles; a click shows the model in 3D (the page renders it with three.js,
// which also makes the tile's picture, kept here in the cache folder).

// builtModel is a model folder, as the gallery lists it.
type builtModel struct {
	Name   string   `json:"name"`   // the folder's
	File   string   `json:"file"`   // the model's file (the OBJ, else the USDZ)
	Format string   `json:"format"` // obj | usdz
	Size   int64    `json:"size"`   // the folder's files, bytes
	At     int64    `json:"at"`     // the model file's time, ms since 1970
	Files  []string `json:"files"`  // all in it
}

// batchModels lists dir's model folders, by name.
func batchModels(dir string) ([]builtModel, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := []builtModel{}
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() || !modelFolderName(n) {
			continue
		}
		if m, ok := modelIn(filepath.Join(dir, n)); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// modelFolderName: a capture's folder n could be a model's (not its samples',
// not the build's own).
func modelFolderName(n string) bool { return cleanName(n) && n != buildDir && n != "samples" }

// modelIn is the model in folder dir, if it holds one.
func modelIn(dir string) (builtModel, bool) {
	m := builtModel{Name: filepath.Base(dir)}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return m, false
	}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		m.Files = append(m.Files, n)
		m.Size += info.Size()
		switch ext := strings.ToLower(filepath.Ext(n)); {
		case ext == ".obj" && m.Format != "obj", ext == ".usdz" && m.Format == "":
			m.File, m.Format, m.At = n, ext[1:], info.ModTime().UnixMilli()
		}
	}
	return m, m.File != ""
}

// modelPath is the model folder named in the request (its capture's and its
// own name), and the model in it.
func modelPath(r *http.Request) (string, builtModel, bool) {
	name, model := r.PathValue("name"), r.PathValue("model")
	if !cleanName(name) || !modelFolderName(model) {
		return "", builtModel{}, false
	}
	dir := filepath.Join(photoDir, name, model)
	m, ok := modelIn(dir)
	return dir, m, ok
}

// handleModels lists a capture's models (GET /batches/{name}/models).
func (a *app) handleModels(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !cleanName(name) {
		http.NotFound(w, r)
		return
	}
	ms, err := batchModels(filepath.Join(photoDir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ms)
}

// handleModelFile serves a model folder's file (GET
// /batches/{name}/models/{model}/{file}): the model, its materials, textures.
func (a *app) handleModelFile(w http.ResponseWriter, r *http.Request) {
	dir, _, ok := modelPath(r)
	file := r.PathValue("file")
	if !ok || !cleanName(file) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(dir, file)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache") // (rebuilt in place: asked again, 304 if the same)
	http.ServeFile(w, r, path)
}

// modelThumbLook: up it when the page draws models' pictures differently
// (the ones kept: made again).
const modelThumbLook = 3

// modelThumbPath is where a model's picture is kept: in the cache folder, by
// the model's path, size and time (a rebuild: a new one).
func modelThumbPath(dir string, m builtModel) string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	sum := sha1.Sum([]byte(fmt.Sprintf("model|%d|%s|%s|%d|%d", modelThumbLook, dir, m.File, m.Size, m.At)))
	return filepath.Join(base, "gosts-rig", "thumbs", hex.EncodeToString(sum[:])+".jpg")
}

// handleModelThumb serves a model's picture (GET
// /batches/{name}/models/{model}/thumb); 404 till the page has made one.
func (a *app) handleModelThumb(w http.ResponseWriter, r *http.Request) {
	dir, m, ok := modelPath(r)
	p := modelThumbPath(dir, m)
	if !ok || p == "" {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

// handleModelThumbPut keeps the picture the page made of a model (PUT
// /batches/{name}/models/{model}/thumb, a JPEG).
func (a *app) handleModelThumbPut(w http.ResponseWriter, r *http.Request) {
	dir, m, ok := modelPath(r)
	p := modelThumbPath(dir, m)
	if !ok || p == "" {
		http.NotFound(w, r)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil || len(data) < 3 || data[0] != 0xFF || data[1] != 0xD8 {
		http.Error(w, "not a JPEG", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
		err = os.WriteFile(p, data, 0o644)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// openFile opens path in its app (macOS' open), or shows it in the Finder
// (reveal). A var: the tests don't.
var openFile = func(path string, reveal bool) error {
	args := []string{path}
	if reveal {
		args = []string{"-R", path}
	}
	if out, err := exec.Command("open", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// handleModelOpen opens a model in its app (the Mac's: Preview, say, or
// whatever's set for it), or with {"reveal": true} shows it in the Finder
// (POST /batches/{name}/models/{model}/open).
func (a *app) handleModelOpen(w http.ResponseWriter, r *http.Request) {
	dir, m, ok := modelPath(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Reveal bool `json:"reveal"`
	}
	json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req)
	if err := openFile(filepath.Join(dir, m.File), req.Reveal); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleModelDelete moves a model folder to the Trash (POST
// /batches/{name}/models/{model}/delete); not one being built into.
func (a *app) handleModelDelete(w http.ResponseWriter, r *http.Request) {
	dir, _, ok := modelPath(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := a.model.building(r.PathValue("name"), r.PathValue("model")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := trash(dir); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// building is an error if a build is going into capture name's folder out.
func (b *modelBuilder) building(name, out string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.Running && b.state.Folder == name && b.state.Out == out {
		return errors.New("a model is being built into it: cancel the build first")
	}
	return nil
}
