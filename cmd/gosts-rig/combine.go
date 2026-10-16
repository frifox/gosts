package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
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
