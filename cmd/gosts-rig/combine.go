package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// combineFrom marks a model saved by Combine Models (edit.json's From).
const combineFrom = "combine"

// handleCombineSave keeps what Combine Models saves (POST /combine/save,
// multipart: folder, to, triangles, obj, stl): a model folder, to, in the
// capture folder (made if it isn't there), its OBJ (mm) and STL. One saved
// there by Combine before under that name goes to the Trash first (saved
// again, as it's worked on); anything else there: refused.
func (a *app) handleCombineSave(w http.ResponseWriter, r *http.Request) {
	fail := func(code int, format string, args ...any) { http.Error(w, fmt.Sprintf(format, args...), code) }
	mr, err := r.MultipartReader()
	if err != nil {
		fail(http.StatusBadRequest, "%v", err)
		return
	}
	var folder, to string
	var tmp string
	triangles := 0
	got := map[string]bool{}
	defer func() {
		if tmp != "" {
			os.RemoveAll(tmp)
		}
	}()
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(http.StatusBadRequest, "%v", err)
			return
		}
		switch field := part.FormName(); field {
		case "folder", "to", "triangles":
			v, _ := io.ReadAll(io.LimitReader(part, 4096))
			s := strings.TrimSpace(string(v))
			switch field {
			case "folder":
				folder = s
			case "to":
				to = s
			case "triangles":
				triangles, _ = strconv.Atoi(s)
			}
		case "obj", "stl":
			if !cleanName(folder) || strings.HasSuffix(folder, "-samples") {
				fail(http.StatusBadRequest, "the folder: one folder name, no slashes, no leading dot")
				return
			}
			if !modelFolderName(to) { // (folder and to come first: the files go by them)
				fail(http.StatusBadRequest, "the name: one folder name, no slashes, no leading dot (not %s, nor samples)", buildDir)
				return
			}
			if tmp == "" {
				if err := os.MkdirAll(filepath.Join(photoDir, folder), 0o755); err != nil {
					fail(http.StatusInternalServerError, "%v", err)
					return
				}
				if tmp, err = os.MkdirTemp(filepath.Join(photoDir, folder), ".save-"); err != nil {
					fail(http.StatusInternalServerError, "%v", err)
					return
				}
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
	if !got["obj"] {
		fail(http.StatusBadRequest, "no OBJ")
		return
	}
	dst := filepath.Join(photoDir, folder, to)
	old := false
	if ents, err := os.ReadDir(dst); err == nil && len(ents) > 0 {
		if e := readModelEdit(dst); e == nil || e.From != combineFrom {
			fail(http.StatusConflict, "%s / %s is there already (not saved from Combine Models): pick another name", folder, to)
			return
		}
		old = true
	}
	edit := modelEdit{From: combineFrom, Units: "mm", Triangles: triangles, STL: got["stl"], At: time.Now().UnixMilli()}
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
	} else {
		os.Remove(dst) // (an empty folder)
	}
	if err := os.Rename(tmp, dst); err != nil {
		fail(http.StatusInternalServerError, "%v", err)
		return
	}
	tmp = ""
	log.Printf("combine: %s / %s saved (%d triangles, mm)", folder, to, triangles)
	m, _ := modelIn(dst)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(m)
}

// Combine Models' work files: what's in it as it's being worked on (its
// objects, where they are, the fill and its cuts: JSON the page makes and
// reads), kept in photoDir as <name>.combine.json, to open again from Browse.
// The models in it are named (capture / model), not copied: one renamed,
// moved or deleted since, the page marks missing.

const workExt = ".combine.json"

// workPath: the work file named, if the name's a fine one.
func workPath(name string) (string, bool) {
	if !cleanName(name) || strings.Contains(name, workExt) {
		return "", false
	}
	return filepath.Join(photoDir, name+workExt), true
}

// workInfo: a work file as Browse lists it.
type workInfo struct {
	Name    string `json:"name"`
	At      int64  `json:"at"`      // last saved (ms)
	Objects int    `json:"objects"` // how many things are in it
	Thumb   bool   `json:"thumb"`   // a picture of it kept
}

// handleWorks lists the work files, the last saved first (GET /combine/works).
func (a *app) handleWorks(w http.ResponseWriter, r *http.Request) {
	ents, err := os.ReadDir(photoDir)
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := []workInfo{}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, workExt) || strings.HasPrefix(n, ".") {
			continue
		}
		wi := workInfo{Name: strings.TrimSuffix(n, workExt)}
		if st, err := e.Info(); err == nil {
			wi.At = st.ModTime().UnixMilli()
		}
		var head struct {
			Objects []json.RawMessage `json:"objects"`
			Thumb   string            `json:"thumb"`
		}
		if b, err := os.ReadFile(filepath.Join(photoDir, n)); err == nil && json.Unmarshal(b, &head) == nil {
			wi.Objects, wi.Thumb = len(head.Objects), head.Thumb != ""
		}
		out = append(out, wi)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleWork sends a work file (GET /combine/works/{name}).
func (a *app) handleWork(w http.ResponseWriter, r *http.Request) {
	p, ok := workPath(r.PathValue("name"))
	if !ok {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, p)
}

// handleWorkThumb sends a work file's picture (GET /combine/works/{name}/thumb):
// a data: URL in it, as the image it holds.
func (a *app) handleWorkThumb(w http.ResponseWriter, r *http.Request) {
	p, ok := workPath(r.PathValue("name"))
	if !ok {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	var f struct {
		Thumb string `json:"thumb"`
	}
	b, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(b, &f) != nil {
		http.NotFound(w, r)
		return
	}
	typ, data, ok := strings.Cut(strings.TrimPrefix(f.Thumb, "data:"), ";base64,")
	if !ok || !strings.HasPrefix(typ, "image/") {
		http.NotFound(w, r)
		return
	}
	img, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(img)
}

// handleWorkSave keeps a work file (POST /combine/works/{name}: its JSON),
// in place of the one by that name if there is one: with ?trash=1 (another
// work replaced: the page asks first), that one to the Trash first; else
// written over (the same work saved again).
func (a *app) handleWorkSave(w http.ResponseWriter, r *http.Request) {
	p, ok := workPath(r.PathValue("name"))
	if !ok {
		http.Error(w, "the name: one file name, no slashes, no leading dot", http.StatusBadRequest)
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !json.Valid(b) {
		http.Error(w, "not JSON", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(photoDir, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmp, err := os.CreateTemp(photoDir, ".work-*.json")
	if err == nil {
		_, err = tmp.Write(b)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil && r.URL.Query().Get("trash") == "1" && fileExists(p) {
			err = trash(p)
		}
		if err == nil {
			err = os.Rename(tmp.Name(), p)
		}
		if err != nil {
			os.Remove(tmp.Name())
		}
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("combine: work %s saved", filepath.Base(p))
	w.WriteHeader(http.StatusNoContent)
}

// handleWorkDelete moves a work file to the Trash (POST /combine/works/{name}/delete).
func (a *app) handleWorkDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := workPath(r.PathValue("name"))
	if !ok {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	if err := trash(p); err != nil {
		code := http.StatusInternalServerError
		if os.IsNotExist(err) {
			code = http.StatusNotFound
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
