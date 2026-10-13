package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestModelOptions: the defaults are good; a bad choice is caught; the
// options become apple-obj-cap's flags (the custom ones only with custom).
func TestModelOptions(t *testing.T) {
	o := defaultModelOptions()
	if err := o.check(); err != nil {
		t.Fatal(err)
	}
	a := o.args()
	if !slices.Contains(a, "full") || slices.Contains(a, "--max-polygons") || slices.Contains(a, "--no-masking") {
		t.Errorf("defaults: %v", a)
	}
	o.Detail, o.Masking, o.Gravity, o.TextureFormat = "custom", false, false, "jpeg"
	a = o.args()
	for _, want := range []string{"--no-masking", "--no-gravity", "--max-polygons", "--texture-maps", "--jpeg-quality"} {
		if !slices.Contains(a, want) {
			t.Errorf("custom: no %s in %v", want, a)
		}
	}
	for _, bad := range []func(*modelOptions){
		func(o *modelOptions) { o.Detail = "huge" },
		func(o *modelOptions) { o.Format = "stl" },
		func(o *modelOptions) { o.Detail, o.MaxPolygons = "custom", 0 },
		func(o *modelOptions) { o.Detail, o.TextureMaps = "custom", nil },
		func(o *modelOptions) { o.Detail, o.TextureMaps = "custom", []string{"shine"} },
		func(o *modelOptions) { o.Out = "../x" },
		func(o *modelOptions) { o.Out = "" },
		func(o *modelOptions) { o.Out = buildDir },
		func(o *modelOptions) { o.Out = "samples" },
	} {
		o := defaultModelOptions()
		bad(&o)
		if o.check() == nil {
			t.Errorf("%+v passed", o)
		}
	}
}

// TestModelOutNotOurs: an output folder with files in it that no build here
// made isn't cleared (it'd go to the Trash): refused.
func TestModelOutNotOurs(t *testing.T) {
	old := photoDir
	photoDir = t.TempDir()
	defer func() { photoDir = old }()
	dir := filepath.Join(photoDir, "cap")
	os.MkdirAll(filepath.Join(dir, "stuff"), 0o755)
	os.WriteFile(filepath.Join(dir, "stuff", "keep.txt"), []byte("x"), 0o644)
	cam := &cameraConn{out: func(any) {}}
	a := &app{camera: cam, cap: &capture{camera: cam, out: func(any) {}}}
	b := &modelBuilder{out: func(any) {}}
	o := defaultModelOptions()
	o.Out = "stuff"
	err := b.start(a, "cap", o)
	if err == nil || !strings.Contains(err.Error(), "has files in it") {
		t.Fatalf("got %v, want refused", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stuff", "keep.txt")); err != nil {
		t.Error("its file's gone")
	}
}

// TestModelNothingSaved: the capture going with no folder yet (nothing
// taken in it) says so, not "no such capture".
func TestModelNothingSaved(t *testing.T) {
	old, oldDir := photoDir, batch.dir
	photoDir = t.TempDir()
	defer func() { photoDir = old; batch.dir = oldDir }()
	batch.dir = filepath.Join(photoDir, "benchy") // (named, not made)
	cam := &cameraConn{out: func(any) {}}
	a := &app{camera: cam, cap: &capture{camera: cam, out: func(any) {}}}
	b := &modelBuilder{out: func(any) {}}
	if err := b.start(a, "benchy", defaultModelOptions()); err == nil || !strings.Contains(err.Error(), "no photo files saved") {
		t.Fatalf("got %v, want no photo files saved", err)
	}
	if err := b.start(a, "other", defaultModelOptions()); err == nil || err.Error() != "no such capture" {
		t.Fatalf("got %v, want no such capture", err)
	}
}
