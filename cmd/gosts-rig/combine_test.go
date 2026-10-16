package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
