package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGostsCam stands in for gosts-cam: each shot answers "fired", then,
// once release is sent to (or at once, if nil), "saved" with a JPEG and a
// DNG; fetched files can be deleted.
type fakeGostsCam struct {
	mu       sync.Mutex
	n        int
	files    map[string][]byte
	deleted  []string
	settings map[string]string
	release  chan struct{} // holds each shot's "saved"
	failNext string        // the next shot fails so, after firing
	nudged   []string
}

func newFakeGostsCam(t *testing.T) (*fakeGostsCam, *httptest.Server) {
	f := &fakeGostsCam{files: map[string][]byte{}, settings: map[string]string{"focus": "Manual", "mode": "M", "shutter": "1/60", "iso": "100", "whitebalance": "Daylight", "resolution": "16 MP", "format": "RAW + JPEG"}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /info", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(piInfo{Model: "Arducam OwlSight 64MP", Sensor: "ov64a40", Version: "1"})
	})
	mux.HandleFunc("POST /shoot", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.n++
		n, fail := f.n, f.failNext
		f.failNext = ""
		release := f.release
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		enc.Encode(piEvent{Event: "fired", At: time.Now()})
		w.(http.Flusher).Flush()
		if release != nil {
			<-release
		}
		if fail != "" {
			enc.Encode(piEvent{Event: "error", Error: fail})
			return
		}
		jpg, dng := fmt.Sprintf("owl_%05d.jpg", n), fmt.Sprintf("owl_%05d.dng", n)
		f.mu.Lock()
		f.files[jpg] = []byte(fmt.Sprintf("JPEG %d", n))
		f.files[dng] = []byte(fmt.Sprintf("DNG %d", n))
		f.mu.Unlock()
		enc.Encode(piEvent{Event: "saved", Files: []string{jpg, dng}})
	})
	mux.HandleFunc("/files/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		name := r.PathValue("name")
		b, ok := f.files[name]
		if !ok {
			http.Error(w, `{"error":"no such file"}`, http.StatusNotFound)
			return
		}
		if r.Method == http.MethodDelete {
			delete(f.files, name)
			f.deleted = append(f.deleted, name)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Write(b)
	})
	mux.HandleFunc("GET /preview", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("PREVIEW"))
	})
	mux.HandleFunc("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode([]cameraSetting{
			{Key: "focus", Label: "Focus", Current: f.settings["focus"], Choices: []string{"Manual", "Automatic"}},
			{Key: "mode", Label: "Exposure mode", Current: f.settings["mode"], Choices: []string{"P", "M"}},
			{Key: "shutter", Label: "Shutter speed", Current: f.settings["shutter"], Choices: []string{"1/125", "1/60", "1/30", "1", "2"}},
			{Key: "iso", Label: "ISO", Current: f.settings["iso"], Choices: []string{"100", "200", "400"}},
			{Key: "whitebalance", Label: "White balance", Current: f.settings["whitebalance"], Choices: []string{"Auto", "Daylight", kelvinMode}},
			{Key: "colortemp", Label: "Color temperature (K)", Current: "5600", Range: true, Min: 2500, Max: 10000, Step: 100, Unit: "K"},
		})
	})
	mux.HandleFunc("POST /settings", func(w http.ResponseWriter, r *http.Request) {
		var kv struct{ Key, Value string }
		json.NewDecoder(r.Body).Decode(&kv)
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.settings[kv.Key]; !ok {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "no setting " + kv.Key})
			return
		}
		f.settings[kv.Key] = kv.Value
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /focus/auto", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "lensPosition": 2.5, "status": "Focus Locked"})
	})
	mux.HandleFunc("POST /focus/nudge", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.nudged = append(f.nudged, r.URL.Query().Get("steps"))
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"lensPosition": 2.75})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv
}

func openFakePi(t *testing.T) (*fakeGostsCam, *piCamera) {
	f, srv := newFakeGostsCam(t)
	cam, name, err := openPiCamera(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if name != "Arducam OwlSight 64MP" {
		t.Errorf("name %q", name)
	}
	t.Cleanup(func() { cam.Close() })
	return f, cam.(*piCamera)
}

type piGot struct {
	p   photo
	err error
}

func piShutter(dir string, fired *int, got chan<- piGot) shutter {
	return shutter{
		firing: func() { *fired++ },
		folder: func() string { return dir },
		got:    func(p photo, err error) { got <- piGot{p, err} },
	}
}

// A shot returns once fired, before its files are saved; they come after,
// in its folder, the JPEG shown, and are deleted on the Pi.
func TestPiShoot(t *testing.T) {
	f, cam := openFakePi(t)
	f.release = make(chan struct{})
	dir := t.TempDir()
	got := make(chan piGot, 4)
	fired := 0
	if err := cam.Shoot(context.Background(), piShutter(dir, &fired, got)); err != nil {
		t.Fatal(err)
	}
	if fired != 1 {
		t.Errorf("firing called %d times", fired)
	}
	select {
	case g := <-got:
		t.Fatalf("got a photo before its files were saved: %+v", g)
	case <-time.After(50 * time.Millisecond):
	}
	close(f.release)
	g := <-got
	if g.err != nil {
		t.Fatal(g.err)
	}
	if string(g.p.JPEG) != "JPEG 1" || len(g.p.Files) != 2 {
		t.Fatalf("photo %q, files %v", g.p.JPEG, g.p.Files)
	}
	for i, want := range []string{"JPEG 1", "DNG 1"} {
		if filepath.Dir(g.p.Files[i]) != dir {
			t.Errorf("%s not in %s", g.p.Files[i], dir)
		}
		if b, _ := os.ReadFile(g.p.Files[i]); string(b) != want {
			t.Errorf("%s: %q, want %q", g.p.Files[i], b, want)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deleted) != 2 || len(f.files) != 0 {
		t.Errorf("deleted %v, left %v", f.deleted, f.files)
	}
}

// Photos come in the order taken; a name taken is not overwritten.
func TestPiShootOrder(t *testing.T) {
	_, cam := openFakePi(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "owl_00001.jpg"), []byte("older"), 0o644)
	got := make(chan piGot, 8)
	fired := 0
	for range 3 {
		if err := cam.Shoot(context.Background(), piShutter(dir, &fired, got)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 3; i++ {
		g := <-got
		if g.err != nil {
			t.Fatal(g.err)
		}
		if want := fmt.Sprintf("JPEG %d", i); string(g.p.JPEG) != want {
			t.Errorf("photo %d: %q", i, g.p.JPEG)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "owl_00001.jpg")); string(b) != "older" {
		t.Errorf("overwrote a file: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "owl_00001-2.jpg")); string(b) != "JPEG 1" {
		t.Errorf("owl_00001-2.jpg: %q", b)
	}
}

// A shot failing after firing: its photo gets the error.
func TestPiShootError(t *testing.T) {
	f, cam := openFakePi(t)
	f.failNext = "out of memory"
	got := make(chan piGot, 1)
	fired := 0
	if err := cam.Shoot(context.Background(), piShutter(t.TempDir(), &fired, got)); err != nil {
		t.Fatal(err)
	}
	if g := <-got; g.err == nil || !strings.Contains(g.err.Error(), "out of memory") {
		t.Errorf("err %v", g.err)
	}
}

// Close lets the photos taken come over first; shooting after fails.
func TestPiClose(t *testing.T) {
	f, cam := openFakePi(t)
	f.release = make(chan struct{})
	got := make(chan piGot, 1)
	fired := 0
	if err := cam.Shoot(context.Background(), piShutter(t.TempDir(), &fired, got)); err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(30 * time.Millisecond); close(f.release) }()
	cam.Close()
	select {
	case g := <-got:
		if g.err != nil {
			t.Error(g.err)
		}
	default:
		t.Error("closed without the photo taken")
	}
	if err := cam.Shoot(context.Background(), piShutter(t.TempDir(), &fired, got)); err == nil {
		t.Error("shot after Close")
	}
}

// Settings come as the dialog has them; the shutter's choices read as
// shutter speeds; setting one sticks, an unknown one fails.
func TestPiSettings(t *testing.T) {
	_, cam := openFakePi(t)
	ss, err := cam.Settings()
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, s := range ss {
		keys = append(keys, s.Key)
		if s.Key == "shutter" {
			for _, c := range s.Choices {
				if _, err := parseShutter(c); err != nil {
					t.Errorf("shutter choice %q: %v", c, err)
				}
			}
		}
	}
	if strings.Join(keys, ",") != "focus,mode,shutter,iso,whitebalance,colortemp" {
		t.Errorf("keys %v", keys)
	}
	if err := cam.Set("shutter", "1/125"); err != nil {
		t.Fatal(err)
	}
	ss, _ = cam.Settings()
	if ss[2].Current != "1/125" {
		t.Errorf("shutter %q after setting", ss[2].Current)
	}
	if err := cam.Set("aperture", "f/8"); err == nil || !strings.Contains(err.Error(), "no setting") {
		t.Errorf("unknown setting: %v", err)
	}
	if got := settingsCaption(ss); got != "1/125 · ISO 100 · Daylight · 5600K · focus: Manual" { // (no F-stop: a fixed lens)
		t.Errorf("caption %q", got)
	}
}

func TestPiFocusAndPreview(t *testing.T) {
	f, cam := openFakePi(t)
	if s, err := cam.Autofocus(); err != nil || s != "Focus Locked" {
		t.Errorf("autofocus %q, %v", s, err)
	}
	if err := cam.Nudge(-3); err != nil {
		t.Error(err)
	}
	if err := cam.Nudge(0); err == nil {
		t.Error("nudge 0 taken")
	}
	if f.nudged[0] != "-3" || len(f.nudged) != 1 {
		t.Errorf("nudged %v", f.nudged)
	}
	if b, err := cam.Preview(); err != nil || string(b) != "PREVIEW" {
		t.Errorf("preview %q, %v", b, err)
	}
}

// Listing: a Pi answering at the configured host is listed; the same Pi
// at two names is listed once; none answering, none listed.
func TestPiDetect(t *testing.T) {
	_, srv := newFakeGostsCam(t)
	host := strings.TrimPrefix(srv.URL, "http://")
	_, port, _ := strings.Cut(host, ":")
	oldHosts, oldCfg := piHosts, piConfigHost
	t.Cleanup(func() { piHosts, piConfigHost = oldHosts, oldCfg })
	piHosts = []string{"localhost:" + port, "127.0.0.1:1"} // the same Pi by name; nothing there
	piConfigHost = host
	cams := piDetect()
	if len(cams) != 1 || cams[0].ID != piPrefix+host || cams[0].Name != "Arducam OwlSight 64MP" {
		t.Errorf("listed %+v, want the Pi once, at %s", cams, host)
	}
	piHosts, piConfigHost = []string{"127.0.0.1:1"}, ""
	if cams := piDetect(); len(cams) != 0 {
		t.Errorf("listed %+v, want none", cams)
	}
}

// A real gosts-cam (GOSTS_PI=host[:port]: the Pi, or gosts-cam --fake):
// its settings, focus, preview, and three photos, end to end.
func TestPiLive(t *testing.T) {
	host := os.Getenv("GOSTS_PI")
	if host == "" {
		t.Skip("GOSTS_PI=host[:port]: a running gosts-cam")
	}
	c, name, err := openPiCamera(host)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cam := c.(*piCamera)
	t.Logf("camera: %s", name)
	ss, err := cam.Settings()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ss {
		t.Logf("%s (%s): %q of %v", s.Label, s.Key, s.Current, s.Choices)
		if s.Key == "shutter" {
			for _, ch := range s.Choices {
				if _, err := parseShutter(ch); err != nil {
					t.Errorf("shutter choice %q: %v", ch, err)
				}
			}
		}
		if !s.Range && len(s.Choices) > 0 && !s.ReadOnly {
			if err := cam.Set(s.Key, s.Current); err != nil {
				t.Errorf("set %s %q: %v", s.Key, s.Current, err)
			}
		}
	}
	t.Logf("caption: %s", settingsCaption(ss))
	if s, err := cam.Autofocus(); err != nil {
		t.Error(err)
	} else {
		t.Logf("autofocus: %s", s)
	}
	if err := cam.Set("focus", "Manual"); err != nil {
		t.Error(err)
	}
	if err := cam.Nudge(2); err != nil {
		t.Error(err)
	}
	if b, err := cam.Preview(); err != nil || len(b) < 2 || b[0] != 0xFF || b[1] != 0xD8 {
		t.Errorf("preview: %d bytes, %v", len(b), err)
	}
	dir := t.TempDir()
	got := make(chan piGot, 3)
	fired := 0
	start := time.Now()
	for range 3 {
		if err := cam.Shoot(context.Background(), piShutter(dir, &fired, got)); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		g := <-got
		if g.err != nil {
			t.Fatal(g.err)
		}
		if len(g.p.JPEG) < 2 || g.p.JPEG[0] != 0xFF {
			t.Errorf("photo not a JPEG: %d bytes", len(g.p.JPEG))
		}
		t.Logf("photo: %v", g.p.Files)
	}
	t.Logf("3 photos in %v; min interval %v", time.Since(start).Round(time.Millisecond), cam.MinInterval())
}
