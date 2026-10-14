package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The settings' folder moves: config.toml written in the new one, a pointer
// in the default folder saying where (so the next start finds it), and back.
// The captures' folder: PhotoDir, applied now.
func TestAppSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // (the user's config directory: in it)
	os.Mkdir(filepath.Join(home, ".Trash"), 0o755) // (the old config.toml goes there when moved)
	oldPhotos := photoDir
	defer func() { photoDir = oldPhotos; newBatch() }()
	cfg, err := loadConfig(defaultConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	cam := &cameraConn{out: func(any) {}}
	a := &app{cfg: cfg, clients: map[*client]struct{}{}, camera: cam, model: &modelBuilder{out: func(any) {}}}
	a.rig = &rig{cfg: cfg, out: func(any) {}}
	a.cap = &capture{rig: a.rig, camera: cam, out: func(any) {}}

	moved := filepath.Join(home, "Sync", "rig")
	if err := a.setSettingsDir(moved); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, "config.toml")); err != nil {
		t.Fatal("not written there:", err)
	}
	if got := defaultConfigPath(); got != filepath.Join(moved, "config.toml") {
		t.Fatalf("the next start would read %s", got)
	}
	if s := a.appSettings(); s.Settings != "~/Sync/rig" {
		t.Fatalf("settings %+v", s)
	}

	pics := filepath.Join(home, "Elsewhere")
	if err := a.setPhotos(pics); err != nil {
		t.Fatal(err)
	}
	if photoDir != pics || cfg.get().PhotoDir != "~/Elsewhere" {
		t.Fatalf("photos %s, PhotoDir %q", photoDir, cfg.get().PhotoDir)
	}
	b, _ := os.ReadFile(filepath.Join(moved, "config.toml"))
	if !strings.Contains(string(b), `PhotoDir = "~/Elsewhere"`) {
		t.Fatal("PhotoDir not saved in the moved settings")
	}
	if err := a.setPhotos(defaultPhotoDir()); err != nil || cfg.get().PhotoDir != "" {
		t.Fatalf("back to the default: %v, %q", err, cfg.get().PhotoDir)
	}

	if err := a.setSettingsDir(defaultSettingsDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(settingsPointer()); !os.IsNotExist(err) {
		t.Fatal("the pointer's still there")
	}
	if got := defaultConfigPath(); got != filepath.Join(defaultSettingsDir(), "config.toml") {
		t.Fatalf("the next start would read %s", got)
	}
}
