package main

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// cameraSetting is one of the camera's settings, for the Config dialog.
type cameraSetting struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Current  string   `json:"current"`
	Choices  []string `json:"choices"`
	ReadOnly bool     `json:"readOnly"`
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
		for _, ch := range c.choices {
			if keepChoice(k.key, ch) || ch == c.current {
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
	return nil
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
		ss = append(ss, cameraSetting{Key: k.key, Label: k.label, Current: s.settings[k.key], Choices: simSettingChoices[k.key]})
	}
	return ss, nil
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
