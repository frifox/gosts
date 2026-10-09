package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"
)

// Config is gosts-rig's settings file: where to listen, which serial port, which
// servo does what, and the last capture plan.
type Config struct {
	ListenAddr string `toml:"ListenAddr"`
	Port       string `toml:"Port,omitempty"` // serial device connected on startup; empty = pick in the browser
	Baud       int    `toml:"Baud,omitzero"`
	Camera     string `toml:"Camera,omitempty"` // camera connected on startup; empty = pick in the browser
	// CameraPace is how often (s) each camera can take a photo and hand it
	// over, keeping that up (measured: see cameraConn.measurePace), by its id.
	CameraPace map[string]float64 `toml:"CameraPace,omitempty"`
	PhotoDir   string             `toml:"PhotoDir,omitempty"` // where real cameras' photos are saved (a folder a day); empty = ~/Pictures/gosts-rig
	Roles      Roles              `toml:"Roles"`
	Motion     Motion             `toml:"Motion"`
	Plan       Plan               `toml:"Plan"`
	Rig        Rig                `toml:"Rig"`
}

// Rig is the rig's measurements in mm, seen from above: X along the base
// sides that carry the posts (and the swing arms), Y along the tilt axis,
// Z up. Only the 3D view uses them.
type Rig struct {
	BaseX        float64 `toml:"BaseX"` // base frame, outer
	BaseY        float64 `toml:"BaseY"`
	PostZ        float64 `toml:"PostZ"`                 // vertical posts in the middle of the X sides
	SwingX       float64 `toml:"SwingX"`                // tilting frame: arm length
	SwingY       float64 `toml:"SwingY"`                // tilting frame: bar length (camera bar)
	CameraX      float64 `toml:"CameraX"`               // the camera's sensor (its ⦵ mark) from the turntable's centre, across, with the arm level
	CameraOffset float64 `toml:"CameraOffset,omitzero"` // (before CameraX: the camera's body from the camera bar; read once into CameraX)
	CameraZ      float64 `toml:"CameraZ"`               // camera up/down from the camera bar, square to the arms; + up
	TurntableZ   float64 `toml:"TurntableZ"`            // turntable top height
	TurntableD   float64 `toml:"TurntableD"`            // turntable diameter
	ObjectZ      float64 `toml:"ObjectZ"`               // object height (the 3D view scales the model to it)
}

// check reports a measurement that can't be drawn.
func (r Rig) check() error {
	for _, v := range []struct {
		name      string
		v, lo, hi float64
	}{{"base X", r.BaseX, 100, 3000}, {"base Y", r.BaseY, 100, 3000}, {"post Z", r.PostZ, 50, 3000},
		{"swing X", r.SwingX, 50, 3000}, {"swing Y", r.SwingY, 50, 3000}, {"camera X", r.CameraX, 50, 3000}, {"camera Z offset", r.CameraZ, -500, 500},
		{"turntable Z", r.TurntableZ, 0, 3000}, {"turntable diameter", r.TurntableD, 20, 2000}, {"object height", r.ObjectZ, 10, 2000}} {
		if v.v < v.lo || v.v > v.hi {
			return fmt.Errorf("%s must be %g–%g mm", v.name, v.lo, v.hi)
		}
	}
	if r.SwingY >= r.BaseY {
		return errors.New("swing Y must be less than base Y: the swing hangs between the posts")
	}
	return nil
}

// Roles says which servo does what. Calibrate the servos in Servo Ctl (the
// page's gosts-ctl console) or gosts-ctl so their own 0° is the reference
// here ("Set 0° to"): the arm level for elevation, the platform's front for
// azimuth.
type Roles struct {
	ElevationLeader   uint8 `toml:"ElevationLeader"`   // top servo, the group's leader
	ElevationFollower uint8 `toml:"ElevationFollower"` // the other top servo
	Azimuth           uint8 `toml:"Azimuth"`           // the platform under the object
	LeaderMirrored    bool  `toml:"LeaderMirrored"`    // faces its partner: turns the other way
	FollowerMirrored  bool  `toml:"FollowerMirrored"`
	InvertElevation   bool  `toml:"InvertElevation"` // positive servo angles lower the camera
	InvertAzimuth     bool  `toml:"InvertAzimuth"`   // positive servo angles turn the platform clockwise (seen from above)
}

// Motion is how the rig moves.
type Motion struct {
	Speed        int     `toml:"Speed"`        // step/s
	Acc          int     `toml:"Acc"`          // 100 step/s²
	ElevationMin float64 `toml:"ElevationMin"` // degrees; the camera never goes outside
	ElevationMax float64 `toml:"ElevationMax"`
}

// Plan is a capture: Photos points spread evenly over the part of the sphere
// round the object that the camera can reach (Motion's elevation range).
type Plan struct {
	Photos   int `toml:"Photos"`
	SettleMS int `toml:"SettleMS"` // wait after arriving, before the shot
	// Moving takes the photos without stopping: the rig keeps moving along
	// one smooth spiral and each photo is taken as it passes its shot (needs
	// a fast shutter). Otherwise the rig stops and settles for each photo.
	Moving bool `toml:"Moving"`
	// Path is how the shots are laid out: PathSphere (spread as evenly as
	// can be over the sphere, on a golden-angle spiral; the default) or
	// PathRings (rings of one elevation each, the elevation changing only
	// between rings; fewer shots in the rings nearer the poles).
	Path string `toml:"Path,omitempty"`
	// ExportFor writes alignment data for that photogrammetry app (see
	// writeExport) into the batch's folder when a capture ends; "": none.
	ExportFor string `toml:"ExportFor,omitempty"`
	// (Before ExportFor: a switch and the app, read once into it.)
	ExportOn bool   `toml:"ExportOn,omitempty,omitzero" json:"-"`
	Export   string `toml:"Export,omitempty" json:"-"`
}

// The paths (Plan.Path).
const (
	PathSphere = "sphere"
	PathRings  = "rings"
)

// path is the plan's path: PathSphere or PathRings (the names before,
// "even" and "linear", read as them).
func (p Plan) path() string {
	switch p.Path {
	case PathRings, "linear":
		return PathRings
	}
	return PathSphere
}

// cameraSensorX is how far (mm) the camera's sensor is in front of its
// body's middle (the A6600: the mount's face 26 mm in front, the sensor 18
// mm behind it), as the 3D view draws it.
const cameraSensorX = 8

func defaultConfig() Config {
	return Config{
		ListenAddr: ":8081",
		Roles:      Roles{ElevationLeader: 1, ElevationFollower: 2, Azimuth: 3, LeaderMirrored: true},
		Motion:     Motion{Speed: 600, Acc: 30, ElevationMin: -45, ElevationMax: 80},
		Plan:       Plan{Photos: 60, SettleMS: 800},
		Rig:        Rig{BaseX: 600, BaseY: 500, PostZ: 400, SwingX: 600, SwingY: 450, CameraX: 332, TurntableZ: 400, TurntableD: 150, ObjectZ: 100},
	}
}

// configFile loads and saves Config.
type configFile struct {
	path string
	mu   sync.Mutex
	c    Config
}

// loadConfig reads path; missing settings get their defaults, and a missing
// file is created so the settings are easy to find.
func loadConfig(path string) (*configFile, error) {
	f := &configFile{path: path, c: defaultConfig()}
	md, err := toml.DecodeFile(path, &f.c)
	if errors.Is(err, fs.ErrNotExist) {
		return f, f.save()
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	changed := false
	if p := f.c.Plan.path(); p != f.c.Plan.Path { // a name from before ("even", "linear", or none): saved as it's called now
		f.c.Plan.Path = p
		changed = true
	}
	if md.IsDefined("Plan", "ExportOn") || md.IsDefined("Plan", "Export") {
		// From before ExportFor: the app, if the switch was on.
		if f.c.Plan.ExportOn && !md.IsDefined("Plan", "ExportFor") {
			f.c.Plan.ExportFor = f.c.Plan.Export
		}
		f.c.Plan.ExportOn, f.c.Plan.Export = false, ""
		changed = true
	}
	if !md.IsDefined("Rig", "CameraX") && md.IsDefined("Rig", "CameraOffset") {
		// From before CameraX: the camera's body CameraOffset along the arms
		// from its bar (SwingX/2 − 10 from the axis), its sensor 8 mm in
		// front of the body's middle.
		r := &f.c.Rig
		r.CameraX = math.Round(r.SwingX/2 - 10 - r.CameraOffset - cameraSensorX)
		r.CameraOffset = 0
		changed = true
	}
	if changed {
		if err := f.save(); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func (f *configFile) get() Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.c
}

// update changes the config and saves it.
func (f *configFile) update(fn func(*Config)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.c)
	return f.save()
}

const configHeader = `# gosts-rig: photogrammetry rig settings. Edited by the web UI; read on startup.
# Servo calibration (zero, tuning, limits) is done in Servo Ctl (gosts-ctl's console, in the Rig settings) and stored on the servos.

`

func (f *configFile) save() error {
	var buf bytes.Buffer
	buf.WriteString(configHeader)
	if err := toml.NewEncoder(&buf).Encode(f.c); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path)
}

// defaultConfigPath is gosts-rig/config.toml in the user's config
// directory. One left from before the rename (gosts/config.toml) is copied
// there the first time.
func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "gosts-rig.toml"
	}
	path := filepath.Join(dir, "gosts-rig", "config.toml")
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if old, err := os.ReadFile(filepath.Join(dir, "gosts", "config.toml")); err == nil {
			if os.MkdirAll(filepath.Dir(path), 0o755) == nil && os.WriteFile(path, old, 0o644) == nil {
				log.Printf("config: copied the settings from before the rename, %s", filepath.Join(dir, "gosts", "config.toml"))
			}
		}
	}
	return path
}
