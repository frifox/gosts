package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"
)

// Config is gosts' settings file: where to listen, which serial port, which
// servo does what, and the last capture plan.
type Config struct {
	ListenAddr string `toml:"ListenAddr"`
	Port       string `toml:"Port,omitempty"` // serial device connected on startup; empty = pick in the browser
	Baud       int    `toml:"Baud,omitzero"`
	Roles      Roles  `toml:"Roles"`
	Motion     Motion `toml:"Motion"`
	Plan       Plan   `toml:"Plan"`
	Rig        Rig    `toml:"Rig"`
}

// Rig is the rig's measurements in mm, seen from above: X along the base
// sides that carry the posts (and the swing arms), Y along the tilt axis,
// Z up. Only the 3D view uses them.
type Rig struct {
	BaseX        float64 `toml:"BaseX"` // base frame, outer
	BaseY        float64 `toml:"BaseY"`
	PostZ        float64 `toml:"PostZ"`        // vertical posts in the middle of the X sides
	SwingX       float64 `toml:"SwingX"`       // tilting frame: arm length
	SwingY       float64 `toml:"SwingY"`       // tilting frame: bar length (camera bar)
	CameraOffset float64 `toml:"CameraOffset"` // camera from the camera bar; + towards the object
	PlatformZ    float64 `toml:"PlatformZ"`    // turntable top height
}

// check reports a measurement that can't be drawn.
func (r Rig) check() error {
	for _, v := range []struct {
		name      string
		v, lo, hi float64
	}{{"base X", r.BaseX, 100, 3000}, {"base Y", r.BaseY, 100, 3000}, {"post Z", r.PostZ, 50, 3000},
		{"swing X", r.SwingX, 50, 3000}, {"swing Y", r.SwingY, 50, 3000}, {"camera offset", r.CameraOffset, -500, 500},
		{"platform Z", r.PlatformZ, 0, 3000}} {
		if v.v < v.lo || v.v > v.hi {
			return fmt.Errorf("%s must be %g–%g mm", v.name, v.lo, v.hi)
		}
	}
	if r.SwingY >= r.BaseY {
		return errors.New("swing Y must be less than base Y: the swing hangs between the posts")
	}
	return nil
}

// Roles says which servo does what. Calibrate the servos in gosts-ctl so
// their own 0° is the reference here ("Set 0° to"): the arm level for
// elevation, the platform's front for azimuth.
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
	// MultiTurn switches the rig's servos to multi-turn mode on connect, so
	// every move goes the short way round: in single-turn mode a move across
	// the servo's 0/360° point goes the long way, nearly a full turn, which
	// could swing the frame into the posts.
	MultiTurn bool `toml:"MultiTurn"`
}

// Plan is a capture: Photos points spread evenly over the part of the sphere
// round the object that the camera can reach (Motion's elevation range).
type Plan struct {
	Photos   int `toml:"Photos"`
	SettleMS int `toml:"SettleMS"` // wait after arriving, before the shot
}

func defaultConfig() Config {
	return Config{
		ListenAddr: ":8081",
		Roles:      Roles{ElevationLeader: 10, ElevationFollower: 11, Azimuth: 12, LeaderMirrored: true},
		Motion:     Motion{Speed: 600, Acc: 30, ElevationMin: -30, ElevationMax: 90, MultiTurn: true},
		Plan:       Plan{Photos: 60, SettleMS: 800},
		Rig:        Rig{BaseX: 600, BaseY: 500, PostZ: 400, SwingX: 600, SwingY: 450, CameraOffset: -50, PlatformZ: 300},
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
	_, err := toml.DecodeFile(path, &f.c)
	if errors.Is(err, fs.ErrNotExist) {
		return f, f.save()
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
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

const configHeader = `# gosts: photogrammetry rig settings. Edited by the web UI; read on startup.
# Servo calibration (zero, tuning, limits) is done in gosts-ctl and stored on the servos.

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

// defaultConfigPath is gosts/config.toml in the user's config directory.
func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "gosts.toml"
	}
	return filepath.Join(dir, "gosts", "config.toml")
}
