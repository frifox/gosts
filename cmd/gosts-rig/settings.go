package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// The app's settings (the header's gear): the captures' folder (PhotoDir,
// Browse's root) and the settings folder (where config.toml is). Each set
// by a command-line flag (--photos, --config) for this run says so.

// flagsSet: --photos and --config were given (main fills it).
var flagsSet = struct{ photos, config bool }{}

type appSettingsMsg struct {
	Photos          string `json:"photos"` // ~ for home
	PhotosDefault   string `json:"photosDefault"`
	PhotosFlag      bool   `json:"photosFlag"`
	Settings        string `json:"settings"`
	SettingsDefault string `json:"settingsDefault"`
	SettingsFlag    bool   `json:"settingsFlag"`
}

func (a *app) appSettings() appSettingsMsg {
	return appSettingsMsg{
		Photos: homeShort(photoDir), PhotosDefault: homeShort(defaultPhotoDir()), PhotosFlag: flagsSet.photos,
		Settings: homeShort(filepath.Dir(a.cfg.filePath())), SettingsDefault: homeShort(defaultSettingsDir()), SettingsFlag: flagsSet.config,
	}
}

// busyWith says what's going on that a folder change would upset ("" if nothing).
func (a *app) busyWith() string {
	switch {
	case a.cap.msg().Running:
		return "a capture is running"
	case a.model.msg().Running:
		return "a model is being built"
	case a.flash.running():
		return "the flash is being measured"
	}
	return ""
}

// setPhotos makes dir the captures' folder, now and from now on (the
// config's PhotoDir): new captures go there, Browse lists it.
func (a *app) setPhotos(dir string) error {
	if why := a.busyWith(); why != "" {
		return fmt.Errorf("%s: change the folder after", why)
	}
	dir, err := filepath.Abs(expandHome(dir))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	keep := homeShort(dir)
	if dir == defaultPhotoDir() {
		keep = "" // (the default: none set)
	}
	if err := a.cfg.update(func(c *Config) { c.PhotoDir = keep }); err != nil {
		return err
	}
	photoDir = dir
	newBatch() // (the next capture's folder: in the new one)
	log.Printf("photos: %s", photoDir)
	a.broadcast(a.camera.batchStatus())
	return nil
}

// setSettingsDir moves the settings (config.toml) to dir and keeps them there:
// a pointer in the default folder says where (none if it's the default).
func (a *app) setSettingsDir(dir string) error {
	if flagsSet.config {
		return errors.New("this run's settings file is set by --config")
	}
	dir, err := filepath.Abs(expandHome(dir))
	if err != nil {
		return err
	}
	old := a.cfg.filePath()
	path := filepath.Join(dir, "config.toml")
	if path == old {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s has a config.toml already: pick another folder (or move that one away)", homeShort(dir))
	}
	if err := a.cfg.moveTo(path); err != nil {
		return err
	}
	if dir == defaultSettingsDir() {
		if err := os.Remove(settingsPointer()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	} else {
		if err := os.MkdirAll(defaultSettingsDir(), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(settingsPointer(), []byte(dir+"\n"), 0o644); err != nil {
			return err
		}
	}
	if err := trash(old); err != nil { // (the old copy: to the Trash)
		log.Printf("settings: the old %s stays: %v", old, err)
	}
	log.Printf("settings: now in %s", path)
	return nil
}

// chooseFolder asks for a folder with macOS' own chooser (gosts-rig runs on
// the Mac the page is on), starting at from; "" if canceled.
func chooseFolder(prompt, from string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("choosing a folder needs macOS")
	}
	from = expandHome(from)
	for from != "/" && from != "." {
		if st, err := os.Stat(from); err == nil && st.IsDir() {
			break
		}
		from = filepath.Dir(from) // (not there: the nearest folder that is)
	}
	out, err := exec.Command("osascript",
		"-e", "on run argv",
		"-e", "activate", // (the chooser in front)
		"-e", "POSIX path of (choose folder with prompt (item 1 of argv) default location (POSIX file (item 2 of argv)))",
		"-e", "end run", prompt, from).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "-128") { // User canceled.
			return "", nil
		}
		return "", fmt.Errorf("choosing a folder: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSuffix(strings.TrimSpace(string(out)), "/"), nil
}
