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
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// A capture's models: its folders with a model in them (an OBJ, or a USDZ),
// Build Model's or put there by hand. The gallery shows them first, as
// tiles; a click shows the model in 3D (the page renders it with three.js,
// which also makes the tile's picture, kept here in the cache folder).

// builtModel is a model folder, as the gallery lists it.
type builtModel struct {
	Name   string     `json:"name"`           // the folder's
	File   string     `json:"file"`           // the model's file (the OBJ, else the USDZ)
	Format string     `json:"format"`         // obj | usdz
	Size   int64      `json:"size"`           // the folder's files, bytes
	At     int64      `json:"at"`             // the model file's time, ms since 1970
	Files  []string   `json:"files"`          // all in it
	Edit   *modelEdit `json:"edit,omitempty"` // a model edited here (leveled, turned, scaled): how
}

// modelEdit is how a model was edited (edit.json, beside it): from which
// model, the change (a 4×4 matrix, three.js' column order, y up, meters),
// the file's units, and what else was made.
type modelEdit struct {
	From      string    `json:"from"`
	Matrix    []float64 `json:"matrix"`
	Units     string    `json:"units"`              // mm | in | m: one unit in the files
	Simplify  int       `json:"simplify,omitempty"` // triangles asked for (0: as it was)
	Triangles int       `json:"triangles"`
	STL       bool      `json:"stl"`
	At        int64     `json:"at"` // ms since 1970
}

// modelEditFile is an edited model's account of itself, in its folder.
const modelEditFile = "edit.json"

// readModelEdit is the edit dir holds, if any.
func readModelEdit(dir string) *modelEdit {
	data, err := os.ReadFile(filepath.Join(dir, modelEditFile))
	if err != nil {
		return nil
	}
	var e modelEdit
	if json.Unmarshal(data, &e) != nil {
		return nil
	}
	return &e
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
	m.Edit = readModelEdit(dir)
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

// textureExts are the image files a model's materials may use.
var textureExts = []string{".png", ".jpg", ".jpeg", ".tif", ".tiff", ".exr", ".bmp", ".tga"}

// handleModelSave saves an edited model (POST
// /batches/{name}/models/{model}/save, multipart): to (the new folder's
// name), edit (its modelEdit, JSON), mtl (the model's materials' file, to
// copy), obj (the edited OBJ) and stl (if made). The folder gets
// <to>.obj, <to>.mtl (the model's, copied), its textures (copied),
// <to>.stl and edit.json. Not over the model itself, nor over a folder
// that isn't an edit saved here (that one: to the Trash first).
func (a *app) handleModelSave(w http.ResponseWriter, r *http.Request) {
	src, _, ok := modelPath(r)
	name := r.PathValue("name")
	if !ok {
		http.NotFound(w, r)
		return
	}
	fail := func(code int, format string, args ...any) { http.Error(w, fmt.Sprintf(format, args...), code) }
	mr, err := r.MultipartReader()
	if err != nil {
		fail(http.StatusBadRequest, "%v", err)
		return
	}
	// The parts to a hidden folder in the capture's; it becomes the model's
	// once all's in.
	tmp, err := os.MkdirTemp(filepath.Join(photoDir, name), ".save-")
	if err != nil {
		fail(http.StatusInternalServerError, "%v", err)
		return
	}
	defer os.RemoveAll(tmp)
	var to, mtl string
	var edit modelEdit
	got := map[string]bool{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(http.StatusBadRequest, "%v", err)
			return
		}
		field := part.FormName()
		switch field {
		case "to", "mtl", "edit":
			v, _ := io.ReadAll(io.LimitReader(part, 1<<16))
			switch field {
			case "to":
				to = strings.TrimSpace(string(v))
			case "mtl":
				mtl = string(v)
			case "edit":
				if err := json.Unmarshal(v, &edit); err != nil {
					fail(http.StatusBadRequest, "edit: %v", err)
					return
				}
			}
		case "obj", "stl":
			if !modelFolderName(to) { // (to comes first: the files are named by it)
				fail(http.StatusBadRequest, "the name: one folder name, no slashes, no leading dot (not %s, nor samples)", buildDir)
				return
			}
			f, err := os.Create(filepath.Join(tmp, to+"."+field))
			if err == nil {
				_, err = io.Copy(f, part)
				if cerr := f.Close(); err == nil {
					err = cerr
				}
			}
			if err != nil {
				fail(http.StatusInternalServerError, "%v", err)
				return
			}
			got[field] = true
		}
	}
	model := r.PathValue("model")
	switch {
	case !got["obj"]:
		fail(http.StatusBadRequest, "no OBJ")
		return
	case to == model:
		fail(http.StatusBadRequest, "save it under another name: %s is the model it's made from", to)
		return
	case !slices.Contains([]string{"mm", "in", "m"}, edit.Units):
		fail(http.StatusBadRequest, "units %q?", edit.Units)
		return
	}
	if err := a.model.building(name, to); err != nil {
		fail(http.StatusConflict, "%v", err)
		return
	}
	dst := filepath.Join(photoDir, name, to)
	old := false
	if ents, err := os.ReadDir(dst); err == nil && len(ents) > 0 {
		if readModelEdit(dst) == nil {
			fail(http.StatusConflict, "%s has files in it (not a model saved here): pick another name", to)
			return
		}
		old = true
	}
	// The materials (renamed as the model) and the textures, copied.
	ents, err := os.ReadDir(src)
	if err != nil {
		fail(http.StatusInternalServerError, "%v", err)
		return
	}
	for _, e := range ents {
		n := e.Name()
		ext := strings.ToLower(filepath.Ext(n))
		var as string
		switch {
		case e.IsDir() || strings.HasPrefix(n, "."):
		case n == mtl && ext == ".mtl":
			as = to + ".mtl"
		case slices.Contains(textureExts, ext):
			as = n
		}
		if as == "" {
			continue
		}
		if err := copyFile(filepath.Join(src, n), filepath.Join(tmp, as)); err != nil {
			fail(http.StatusInternalServerError, "%v", err)
			return
		}
	}
	edit.From, edit.STL = model, got["stl"]
	edit.At = time.Now().UnixMilli()
	data, _ := json.MarshalIndent(edit, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, modelEditFile), data, 0o644); err != nil {
		fail(http.StatusInternalServerError, "%v", err)
		return
	}
	if old {
		if err := trash(dst); err != nil {
			fail(http.StatusInternalServerError, "the one saved before couldn't go to the Trash: %v", err)
			return
		}
		log.Printf("model: %s's %s from before moved to the Trash", name, to)
	} else {
		os.Remove(dst) // (an empty folder)
	}
	if err := os.Rename(tmp, dst); err != nil {
		fail(http.StatusInternalServerError, "%v", err)
		return
	}
	log.Printf("model: %s's %s saved as %s (%s, %d triangles)", name, model, to, edit.Units, edit.Triangles)
	m, _ := modelIn(dst)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(m)
}

// copyFile copies a file's bytes.
func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
