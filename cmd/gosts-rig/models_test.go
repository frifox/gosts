package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A capture's models: its folders with an OBJ or a USDZ (not samples, not
// the build's own); their files served, nothing outside them; a picture
// kept; opened in their app; deleted to the Trash, not while built into.
func TestModels(t *testing.T) {
	old := photoDir
	photoDir = t.TempDir()
	defer func() { photoDir = old }()
	home := t.TempDir() // (the cache and the Trash: not the user's)
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".Trash"), 0o755)
	var opened []string
	defer func(f func(string, bool) error) { openFile = f }(openFile)
	openFile = func(p string, reveal bool) error {
		if reveal {
			p = "reveal " + p
		}
		opened = append(opened, p)
		return nil
	}

	dir := filepath.Join(photoDir, "cap")
	put := func(rel, data string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(data), 0o644)
	}
	put("a.JPG", "jpg")
	put("model/m.obj", "v 0 0 0")
	put("model/m.mtl", "newmtl g0")
	put("model/m.usda", "usda")
	put("quick/q.usdz", "usdz")
	put("notes/readme.txt", "x")
	put("samples/s.obj", "v 0 0 0")
	put(buildDir+"/b.obj", "v 0 0 0")

	a := &app{model: &modelBuilder{out: func(any) {}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /batches/{name}/models", a.handleModels)
	mux.HandleFunc("GET /batches/{name}/models/{model}/{file}", a.handleModelFile)
	mux.HandleFunc("GET /batches/{name}/models/{model}/thumb", a.handleModelThumb)
	mux.HandleFunc("PUT /batches/{name}/models/{model}/thumb", a.handleModelThumbPut)
	mux.HandleFunc("POST /batches/{name}/models/{model}/open", a.handleModelOpen)
	mux.HandleFunc("POST /batches/{name}/models/{model}/delete", a.handleModelDelete)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)))
		return w
	}

	w := do("GET", "/batches/cap/models", "")
	var ms []builtModel
	json.Unmarshal(w.Body.Bytes(), &ms)
	if len(ms) != 2 || ms[0].Name != "model" || ms[0].File != "m.obj" || ms[0].Format != "obj" || len(ms[0].Files) != 3 ||
		ms[1].Name != "quick" || ms[1].Format != "usdz" {
		t.Fatalf("models: %s", w.Body)
	}

	if w := do("GET", "/batches/cap/models/model/m.mtl", ""); w.Code != 200 || w.Body.String() != "newmtl g0" {
		t.Errorf("a model's file: %d %q", w.Code, w.Body)
	}
	for _, p := range []string{"/batches/cap/models/model/..%2Fa.JPG", "/batches/cap/models/notes/readme.txt", "/batches/cap/models/samples/s.obj", "/batches/cap/models/model/nope.png"} {
		if w := do("GET", p, ""); w.Code != 404 {
			t.Errorf("%s: %d, want 404", p, w.Code)
		}
	}

	if w := do("GET", "/batches/cap/models/model/thumb", ""); w.Code != 404 {
		t.Errorf("no picture yet: %d", w.Code)
	}
	if w := do("PUT", "/batches/cap/models/model/thumb", "not a jpeg"); w.Code != 400 {
		t.Errorf("not a JPEG kept: %d", w.Code)
	}
	jpg := "\xff\xd8\xff picture"
	do("PUT", "/batches/cap/models/model/thumb", jpg)
	if w := do("GET", "/batches/cap/models/model/thumb", ""); w.Code != 200 || w.Body.String() != jpg {
		t.Errorf("the picture: %d %q", w.Code, w.Body)
	}

	do("POST", "/batches/cap/models/model/open", "")
	do("POST", "/batches/cap/models/quick/open", `{"reveal":true}`)
	if want := []string{filepath.Join(dir, "model", "m.obj"), "reveal " + filepath.Join(dir, "quick", "q.usdz")}; len(opened) != 2 || opened[0] != want[0] || opened[1] != want[1] {
		t.Errorf("opened %q, want %q", opened, want)
	}

	a.model.state = modelState{Folder: "cap", Out: "model", Running: true}
	if w := do("POST", "/batches/cap/models/model/delete", ""); w.Code != http.StatusConflict {
		t.Errorf("deleted while built into: %d", w.Code)
	}
	a.model.state.Running = false
	if w := do("POST", "/batches/cap/models/model/delete", ""); w.Code != 204 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, "model")); !os.IsNotExist(err) {
		t.Error("the model's folder is still there")
	}
	if _, err := os.Stat(filepath.Join(home, ".Trash", "model", "m.obj")); err != nil {
		t.Error("not in the Trash:", err)
	}
}

// An edited model saved: its OBJ (and STL) as sent, the model's materials
// (renamed) and textures copied, edit.json; again over it (the one before
// to the Trash); not over the model itself, nor a folder not saved here.
func TestModelSave(t *testing.T) {
	old := photoDir
	photoDir = t.TempDir()
	defer func() { photoDir = old }()
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".Trash"), 0o755)
	dir := filepath.Join(photoDir, "cap")
	for rel, data := range map[string]string{"model-full/m.obj": "v 0 0 0", "model-full/m.mtl": "map_Kd tex.png", "model-full/tex.png": "png",
		"model-full/m.usda": "usda", "mine/keep.txt": "x"} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755)
		os.WriteFile(filepath.Join(dir, rel), []byte(data), 0o644)
	}
	a := &app{model: &modelBuilder{out: func(any) {}}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /batches/{name}/models/{model}/save", a.handleModelSave)
	save := func(to string, stl bool) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		mw.WriteField("to", to)
		mw.WriteField("mtl", "m.mtl")
		mw.WriteField("edit", `{"matrix":[1,0,0,0,0,1,0,0,0,0,1,0,0,0,0,1],"units":"mm","triangles":1}`)
		f, _ := mw.CreateFormFile("obj", to+".obj")
		f.Write([]byte("mtllib " + to + ".mtl\nv 1 2 3\n"))
		if stl {
			f, _ = mw.CreateFormFile("stl", to+".stl")
			f.Write([]byte("stl"))
		}
		mw.Close()
		req := httptest.NewRequest("POST", "/batches/cap/models/model-full/save", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	if w := save("model-full-aligned", true); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	out := filepath.Join(dir, "model-full-aligned")
	var names []string
	ents, _ := os.ReadDir(out)
	for _, e := range ents {
		names = append(names, e.Name())
	}
	want := []string{"edit.json", "model-full-aligned.mtl", "model-full-aligned.obj", "model-full-aligned.stl", "tex.png"}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Fatalf("saved %v, want %v", names, want)
	}
	if data, _ := os.ReadFile(filepath.Join(out, "model-full-aligned.mtl")); string(data) != "map_Kd tex.png" {
		t.Errorf("materials: %q", data)
	}
	e := readModelEdit(out)
	if e == nil || e.From != "model-full" || e.Units != "mm" || !e.STL || len(e.Matrix) != 16 {
		t.Errorf("edit.json: %+v", e)
	}

	if w := save("model-full-aligned", false); w.Code != 200 {
		t.Fatalf("again: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(out, "model-full-aligned.stl")); !os.IsNotExist(err) {
		t.Error("the STL from before is still there")
	}
	if _, err := os.Stat(filepath.Join(home, ".Trash", "model-full-aligned", "model-full-aligned.stl")); err != nil {
		t.Error("the one before isn't in the Trash:", err)
	}
	for _, to := range []string{"model-full", "mine", "model-build", "../x"} {
		if w := save(to, false); w.Code == 200 {
			t.Errorf("saved over %s", to)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "mine", "keep.txt")); string(data) != "x" {
		t.Error("mine's file is gone")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 3 {
		t.Errorf("left in the capture: %v", ents)
	}
}
