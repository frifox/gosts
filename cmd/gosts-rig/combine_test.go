package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Combine Models' Save: a model folder in the capture folder named (made
// if need be); saved again, the one before to the Trash; a model it didn't
// save there: left alone (refused); a bad name: refused.
func TestCombineSave(t *testing.T) {
	old := photoDir
	photoDir = t.TempDir()
	defer func() { photoDir = old }()
	t.Setenv("HOME", t.TempDir()) // (the Trash: the test's own)
	os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".Trash"), 0o755)
	a := &app{}
	save := func(folder, to, obj string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		mw.WriteField("folder", folder)
		mw.WriteField("to", to)
		mw.WriteField("triangles", "12")
		fw, _ := mw.CreateFormFile("obj", to+".obj")
		fw.Write([]byte(obj))
		fw, _ = mw.CreateFormFile("stl", to+".stl")
		fw.Write([]byte("solid x\nendsolid x\n"))
		mw.Close()
		r := httptest.NewRequest(http.MethodPost, "/combine/save", &body)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		w := httptest.NewRecorder()
		a.handleCombineSave(w, r)
		return w
	}
	if w := save("inserts", "insert", "v 0 0 0\n"); w.Code != http.StatusOK {
		t.Fatalf("first save: %d %s", w.Code, w.Body)
	}
	dir := filepath.Join(photoDir, "inserts", "insert")
	for _, f := range []string{"insert.obj", "insert.stl", modelEditFile} {
		if !fileExists(filepath.Join(dir, f)) {
			t.Errorf("%s not saved", f)
		}
	}
	if e := readModelEdit(dir); e == nil || e.From != combineFrom || e.Units != "mm" || e.Triangles != 12 || !e.STL {
		t.Errorf("edit.json: %+v", e)
	}
	if w := save("inserts", "insert", "v 1 1 1\n"); w.Code != http.StatusOK {
		t.Fatalf("saved again: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "insert.obj")); string(b) != "v 1 1 1\n" {
		t.Errorf("saved again, the OBJ: %q", b)
	}
	if ents, _ := os.ReadDir(filepath.Join(os.Getenv("HOME"), ".Trash")); len(ents) != 1 {
		t.Errorf("the one before: %d in the Trash, want 1", len(ents))
	}
	// A model not saved by Combine: refused.
	other := filepath.Join(photoDir, "inserts", "scan")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "scan.obj"), []byte("v 0 0 0\n"), 0o644)
	if w := save("inserts", "scan", "v 2 2 2\n"); w.Code != http.StatusConflict {
		t.Errorf("over another model: %d, want 409", w.Code)
	}
	if w := save("../x", "insert", "v 0 0 0\n"); w.Code != http.StatusBadRequest {
		t.Errorf("a bad folder: %d, want 400", w.Code)
	}
	if left, _ := filepath.Glob(filepath.Join(photoDir, "inserts", ".save-*")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

// Combine Models' work files: saved (again: replaced), listed with how many
// objects and its picture, sent back, the picture as an image, to the
// Trash; a bad name or body: refused.
func TestCombineWorks(t *testing.T) {
	old := photoDir
	photoDir = t.TempDir()
	defer func() { photoDir = old }()
	t.Setenv("HOME", t.TempDir())
	os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".Trash"), 0o755)
	a := &app{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /combine/works", a.handleWorks)
	mux.HandleFunc("GET /combine/works/{name}", a.handleWork)
	mux.HandleFunc("GET /combine/works/{name}/thumb", a.handleWorkThumb)
	mux.HandleFunc("POST /combine/works/{name}", a.handleWorkSave)
	mux.HandleFunc("POST /combine/works/{name}/delete", a.handleWorkDelete)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	png := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG fake"))
	if w := do("POST", "/combine/works/case%20insert", `{"version":1,"objects":[{"id":1},{"id":2}],"thumb":"`+png+`"}`); w.Code != http.StatusNoContent {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/combine/works/case%20insert", `{"version":1,"objects":[{"id":1}],"thumb":"`+png+`"}`); w.Code != http.StatusNoContent {
		t.Fatalf("saved again: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/combine/works/case%20insert?trash=1", `{"version":1,"objects":[{"id":1},{"id":2},{"id":3}],"thumb":"`+png+`"}`); w.Code != http.StatusNoContent {
		t.Fatalf("another in its place: %d %s", w.Code, w.Body)
	}
	var list []workInfo
	json.Unmarshal(do("GET", "/combine/works", "").Body.Bytes(), &list)
	if len(list) != 1 || list[0].Name != "case insert" || list[0].Objects != 3 || !list[0].Thumb {
		t.Errorf("list: %+v", list)
	}
	if w := do("GET", "/combine/works/case%20insert", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":3`) {
		t.Errorf("read: %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/combine/works/case%20insert/thumb", ""); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || w.Body.String() != "\x89PNG fake" {
		t.Errorf("thumb: %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	if w := do("POST", "/combine/works/x", "not json"); w.Code != http.StatusBadRequest {
		t.Errorf("not JSON: %d", w.Code)
	}
	if w := do("POST", "/combine/works/.hidden", "{}"); w.Code != http.StatusBadRequest {
		t.Errorf("a bad name: %d", w.Code)
	}
	if w := do("POST", "/combine/works/case%20insert/delete", ""); w.Code != http.StatusNoContent {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	if fileExists(filepath.Join(photoDir, "case insert"+workExt)) {
		t.Error("still there after delete")
	}
	if ents, _ := os.ReadDir(filepath.Join(os.Getenv("HOME"), ".Trash")); len(ents) != 2 {
		t.Errorf("the Trash: %d, want 2 (the one replaced, the one deleted)", len(ents))
	}
	if left, _ := filepath.Glob(filepath.Join(photoDir, ".work-*")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}
