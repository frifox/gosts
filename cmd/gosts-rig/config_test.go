package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Alignment data: Apple Object Capture's unless chosen; a choice kept (None
// too); a config from before ExportFor with its switch off: none.
func TestConfigExportFor(t *testing.T) {
	dir := t.TempDir()
	load := func(name, toml string) string {
		p := filepath.Join(dir, name)
		if toml != "" {
			os.WriteFile(p, []byte(toml), 0o644)
		}
		f, err := loadConfig(p)
		if err != nil {
			t.Fatal(err)
		}
		return f.get().Plan.ExportFor
	}
	if got := load("new.toml", ""); got != ExportApple {
		t.Errorf("a new config: %q, want %q", got, ExportApple)
	}
	if got := load("new.toml", ""); got != ExportApple { // (as saved)
		t.Errorf("a new config, again: %q", got)
	}
	if got := load("none.toml", "[Plan]\nExportFor = \"\"\n"); got != "" {
		t.Errorf("None chosen: %q", got)
	}
	if got := load("colmap.toml", "[Plan]\nExportFor = \"colmap\"\n"); got != "colmap" {
		t.Errorf("COLMAP chosen: %q", got)
	}
	if got := load("old-off.toml", "[Plan]\nExportOn = false\nExport = \"metashape\"\n"); got != "" {
		t.Errorf("from before, off: %q", got)
	}
	if got := load("old-on.toml", "[Plan]\nExportOn = true\nExport = \"metashape\"\n"); got != "metashape" {
		t.Errorf("from before, on: %q", got)
	}
	if got := load("none-again.toml", "[Plan]\nPhotos = 100\n"); got != ExportApple {
		t.Errorf("a config without it: %q", got)
	}
	f, _ := loadConfig(filepath.Join(dir, "none.toml"))
	f.update(func(c *Config) { c.Plan.ExportFor = "" })
	if got := load("none.toml", ""); got != "" {
		t.Errorf("None saved and read again: %q", got)
	}
}
