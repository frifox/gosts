package main

import (
	"errors"
	"fmt"
	"log"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// cameraSetting is one of the camera's settings, for the Config dialog.
type cameraSetting struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Current  string   `json:"current"`
	Choices  []string `json:"choices"`
	ReadOnly bool     `json:"readOnly"`
	Probe    bool     `json:"probe,omitempty"`  // the choices the lens has can be found (see proberCamera)
	Probed   bool     `json:"probed,omitempty"` // and were: Choices are only those
}

// proberCamera is a camera that can find which of a setting's choices really
// work (the f-stops the lens has): Probe tries them out, leaves the setting
// as it was, and Settings offers only those from then on.
type proberCamera interface {
	Probe(key string) error
}

// settingsCamera is a camera whose settings can be read and changed.
type settingsCamera interface {
	Settings() ([]cameraSetting, error)
	Set(key, value string) error
}

// settingKeys are the settings the Config dialog offers, in its order: the
// exposure mode first (shutter, f-stop and ISO only hold in the modes that
// leave them to you: M for all three).
var settingKeys = []struct{ key, label string }{
	{"mode", "Exposure mode"},
	{"shutter", "Shutter speed"},
	{"aperture", "F-stop"},
	{"iso", "ISO"},
	{"whitebalance", "White balance"},
	{"focus", "Focus"},
}

// keepChoice drops the choices the dialog doesn't offer: ISO's multi-frame
// noise reduction variants, and the exposure modes other than P, A, S, M.
func keepChoice(key, choice string) bool {
	switch key {
	case "iso":
		return !strings.Contains(choice, "Multi Frame")
	case "mode":
		return slices.Contains([]string{"P", "A", "S", "M"}, choice)
	}
	return true
}

// settingsCaption is a photo's settings in short, e.g. "1/60 · f/8 · ISO 200 ·
// Daylight · Manual".
func settingsCaption(ss []cameraSetting) string {
	var parts []string
	for _, s := range ss {
		switch s.Key {
		case "mode":
			continue
		case "iso":
			if _, err := strconv.Atoi(s.Current); err == nil {
				parts = append(parts, "ISO "+s.Current)
				continue
			}
		case "focus":
			parts = append(parts, "focus: "+s.Current)
			continue
		}
		parts = append(parts, s.Current)
	}
	return strings.Join(parts, " · ")
}

// ---------------------------------------------------------------- gphoto2

// gphoto2Settings are where gphoto2 has the settings (as on the A6600).
var gphoto2Settings = map[string]string{
	"mode":         "/main/capturesettings/expprogram",
	"shutter":      "/main/capturesettings/shutterspeed",
	"aperture":     "/main/capturesettings/f-number",
	"iso":          "/main/imgsettings/iso",
	"whitebalance": "/main/imgsettings/whitebalance",
	"focus":        "/main/capturesettings/focusmode",
}

// gphoto2Config is what get-config says about a setting.
type gphoto2Config struct {
	label, current string
	readOnly       bool
	choices        []string // in the camera's order: the index is set-config-index's
}

// parseGphoto2Config reads get-config's output.
func parseGphoto2Config(out string) (gphoto2Config, error) {
	var c gphoto2Config
	found := false
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		if i := strings.LastIndex(l, "\r"); i >= 0 {
			l = l[i+1:]
		}
		switch {
		case strings.HasPrefix(l, "Label: "):
			c.label, found = strings.TrimPrefix(l, "Label: "), true
		case strings.HasPrefix(l, "Readonly: "):
			c.readOnly = strings.TrimPrefix(l, "Readonly: ") == "1"
		case strings.HasPrefix(l, "Current: "):
			c.current = strings.TrimPrefix(l, "Current: ")
		case strings.HasPrefix(l, "Choice: "):
			// "Choice: 12 f/4": the index, then the value.
			if _, v, ok := strings.Cut(strings.TrimPrefix(l, "Choice: "), " "); ok {
				c.choices = append(c.choices, v)
			}
		}
	}
	if !found {
		return c, fmt.Errorf("unexpected answer: %s", strings.TrimSpace(out))
	}
	return c, nil
}

// Settings reads the dialog's settings from the camera.
func (g *gphoto2Camera) Settings() ([]cameraSetting, error) {
	var lines []string
	for _, k := range settingKeys {
		lines = append(lines, "get-config "+gphoto2Settings[k.key])
	}
	outs, err := g.runLines(lines)
	if err != nil {
		return nil, err
	}
	var ss []cameraSetting
	for i, k := range settingKeys {
		c, err := parseGphoto2Config(outs[i])
		if err != nil {
			continue // a camera without it: not offered
		}
		s := cameraSetting{Key: k.key, Label: k.label, Current: c.current, ReadOnly: c.readOnly}
		lo, hi := 0, len(c.choices)-1
		if k.key == "aperture" {
			s.Probe = true
			g.mu.Lock()
			if g.apertures[1] > 0 { // probed: the lens's range
				lo, hi, s.Probed = g.apertures[0], g.apertures[1], true
			}
			g.mu.Unlock()
		}
		for j, ch := range c.choices {
			if (j >= lo && j <= hi && keepChoice(k.key, ch)) || ch == c.current {
				s.Choices = append(s.Choices, ch)
			}
		}
		ss = append(ss, s)
	}
	return ss, nil
}

// Set changes one setting (by its index among the camera's choices: some
// values have spaces).
func (g *gphoto2Camera) Set(key, value string) error {
	path, ok := gphoto2Settings[key]
	if !ok {
		return fmt.Errorf("no setting %q", key)
	}
	outs, err := g.runLines([]string{"get-config " + path})
	if err != nil {
		return err
	}
	c, err := parseGphoto2Config(outs[0])
	if err != nil {
		return err
	}
	i := slices.Index(c.choices, value)
	if i < 0 {
		return fmt.Errorf("%s: no choice %q", key, value)
	}
	outs, err = g.runLines([]string{fmt.Sprintf("set-config-index %s=%d", path, i)})
	if err != nil {
		return err
	}
	if strings.Contains(outs[0], "*** Error") || strings.Contains(strings.ToLower(outs[0]), "failed") {
		return fmt.Errorf("the camera refused %s %s: %s", key, value, gphoto2Error([]byte(outs[0])))
	}
	// It takes a moment: reading straight back still gives the old value.
	if err := g.awaitSetting(path, value, settingWait); err != nil {
		return fmt.Errorf("the camera didn't take %s %s (maybe not in this exposure mode): %w", key, value, err)
	}
	return nil
}

// settingWait is how long a setting gets to take on the camera.
var settingWait = 5 * time.Second

// Probe finds the f-stops the lens has. The camera reports every f-stop there
// is, whatever the lens; but it sets one by stepping the aperture towards it,
// stopping where the lens does: so asking for the widest and the narrowest
// there are leaves it at the lens's limits. Then the aperture goes back to
// where it was. Slow: the camera steps a click at a time (some 30 s).
func (g *gphoto2Camera) Probe(key string) error {
	if key != "aperture" {
		return fmt.Errorf("%s can't be probed", key)
	}
	path := gphoto2Settings[key]
	get := func() (gphoto2Config, error) {
		outs, err := g.runLines([]string{"get-config " + path})
		if err != nil {
			return gphoto2Config{}, err
		}
		return parseGphoto2Config(outs[0])
	}
	set := func(i int) error {
		_, err := g.runLines([]string{fmt.Sprintf("set-config-index %s=%d", path, i)})
		return err
	}
	c, err := get()
	if err != nil {
		return err
	}
	was := slices.Index(c.choices, c.current)
	limit := func(i int) (int, error) { // where it stops, asked for choice i
		if err := set(i); err != nil {
			return 0, err
		}
		got, err := get()
		if err != nil {
			return 0, err
		}
		j := slices.Index(c.choices, got.current)
		if j < 0 {
			return 0, fmt.Errorf("the camera's at %q, not one of its choices", got.current)
		}
		return j, nil
	}
	lo, err1 := limit(0)
	hi, err2 := limit(len(c.choices) - 1)
	if was >= 0 {
		set(was) // back as it was, whatever happened
	}
	if err := errors.Join(err1, err2); err != nil {
		return err
	}
	if lo > hi {
		return fmt.Errorf("odd lens range: %s to %s", c.choices[lo], c.choices[hi])
	}
	g.mu.Lock()
	g.apertures = [2]int{lo, hi}
	g.mu.Unlock()
	log.Printf("camera: the lens has %s to %s", c.choices[lo], c.choices[hi])
	return nil
}

// Battery is the camera's battery level, as it says (e.g. "92%").
func (g *gphoto2Camera) Battery() (string, error) {
	outs, err := g.runLines([]string{"get-config /main/status/batterylevel"})
	if err != nil {
		return "", err
	}
	c, err := parseGphoto2Config(outs[0])
	if err != nil {
		return "", err
	}
	return c.current, nil
}

// ---------------------------------------------------------------- simulator

// simSettingChoices are the simulated camera's settings and choices.
var simSettingChoices = map[string][]string{
	"mode":         {"P", "A", "S", "M"},
	"shutter":      {"1/8", "1/15", "1/30", "1/60", "1/125", "1/250", "1/500", "1/1000"},
	"aperture":     {"f/2.8", "f/4", "f/5.6", "f/8", "f/11", "f/16"},
	"iso":          {"Auto ISO", "100", "200", "400", "800", "1600", "3200"},
	"whitebalance": {"Automatic", "Daylight", "Cloudy", "Tungsten", "Fluorescent", "Flash"},
	"focus":        {"Manual", "Automatic"},
}

func (s *simCamera) Settings() ([]cameraSetting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings == nil {
		s.settings = map[string]string{"mode": "M", "shutter": "1/60", "aperture": "f/8", "iso": "200", "whitebalance": "Automatic", "focus": "Manual"}
	}
	var ss []cameraSetting
	for _, k := range settingKeys {
		st := cameraSetting{Key: k.key, Label: k.label, Current: s.settings[k.key], Choices: simSettingChoices[k.key]}
		if k.key == "aperture" {
			st.Probe, st.Probed = true, s.probed
			if s.probed {
				st.Choices = simLens
			}
		}
		ss = append(ss, st)
	}
	return ss, nil
}

// simLens is the simulated camera's lens: f/4 to f/16 (found by Probe).
var simLens = []string{"f/4", "f/5.6", "f/8", "f/11", "f/16"}

// simProbeTime is how long probing the simulated lens takes.
var simProbeTime = 3 * time.Second

func (s *simCamera) Probe(key string) error {
	if key != "aperture" {
		return fmt.Errorf("%s can't be probed", key)
	}
	time.Sleep(simProbeTime) // stepping the aperture, as a real camera would
	s.mu.Lock()
	s.probed = true
	s.mu.Unlock()
	return nil
}

func (s *simCamera) Set(key, value string) error {
	if !slices.Contains(simSettingChoices[key], value) {
		return fmt.Errorf("%s: no choice %q", key, value)
	}
	s.Settings() // the defaults, first time
	s.mu.Lock()
	s.settings[key] = value
	s.mu.Unlock()
	return nil
}

// exposure is how much brighter (or darker, under 1) the simulated photo
// comes out than at 1/60 s, f/8, ISO 200: the light it lets in. In the
// automatic modes (P, A, S) or with Auto ISO the camera evens it out: 1.
func (s *simCamera) exposure() float64 {
	ss, _ := s.Settings()
	get := func(k string) string {
		for _, x := range ss {
			if x.Key == k {
				return x.Current
			}
		}
		return ""
	}
	if get("mode") != "M" || get("iso") == "Auto ISO" {
		return 1
	}
	t, err1 := parseShutter(get("shutter"))
	n, err2 := strconv.ParseFloat(strings.TrimPrefix(get("aperture"), "f/"), 64)
	iso, err3 := strconv.ParseFloat(get("iso"), 64)
	if err := errors.Join(err1, err2, err3); err != nil || n <= 0 {
		return 1
	}
	ref := (1.0 / 60) * 200 / (8 * 8)
	return math.Max(1.0/32, math.Min(32, t*iso/(n*n)/ref))
}

// parseShutter reads a shutter speed: "1/60", "30", "32/10".
func parseShutter(v string) (float64, error) {
	if a, b, ok := strings.Cut(v, "/"); ok {
		x, err1 := strconv.ParseFloat(a, 64)
		y, err2 := strconv.ParseFloat(b, 64)
		if err := errors.Join(err1, err2); err != nil || y == 0 {
			return 0, fmt.Errorf("shutter speed %q", v)
		}
		return x / y, nil
	}
	return strconv.ParseFloat(v, 64)
}
