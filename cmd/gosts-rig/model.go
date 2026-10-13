package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Building a 3D model from a capture's photos (Browse → Build Model), with
// Apple's Object Capture through apple-obj-cap (cmd/apple-obj-cap: a Swift
// tool, as Object Capture is Swift only). One build at a time. The model
// goes to the capture's model folder (OBJ, or model.usdz): Object
// Capture writes only into an empty folder, so a model there from before
// goes to the Trash first. The rest is in model-build: the options
// (config.json), the checkpoints (a build again is quicker), the photos'
// camera poses, and, when it ends, how each photo fared (status.json), so
// the marks are there when the folder's opened again.

// buildDir is a capture's folder for the build (its config, checkpoints,
// poses, the photos' status); the model goes to another (defaultOut).
const buildDir = "model-build"

// defaultOut is the model's folder unless one's given: model-<detail>
// (model-full, say).
func defaultOut(detail string) string { return "model-" + detail }

// modelOptions are apple-obj-cap's (see its --help), as the page sets them.
type modelOptions struct {
	Out           string   `json:"out"`         // the model's folder in the capture's (default model-<detail>)
	Format        string   `json:"format"`      // obj | usdz
	Detail        string   `json:"detail"`      // preview | reduced | medium | full | raw | custom
	Ordering      string   `json:"ordering"`    // sequential | unordered
	Sensitivity   string   `json:"sensitivity"` // normal | high
	Masking       bool     `json:"masking"`
	IgnoreBBox    bool     `json:"ignoreBBox"`
	Mesh          string   `json:"mesh"` // triangle | quad
	Checkpoint    bool     `json:"checkpoint"`
	Gravity       bool     `json:"gravity"` // the rig's gravity.json (alignment data for Apple), if the capture has one
	Bounds        bool     `json:"bounds"`
	PointCloud    bool     `json:"pointCloud"`
	MaxPolygons   int      `json:"maxPolygons"`   // custom
	TextureDim    string   `json:"textureDim"`    // custom: 1k … 16k
	TextureMaps   []string `json:"textureMaps"`   // custom: diffuse, normal, roughness, displacement, ao, all
	TextureFormat string   `json:"textureFormat"` // custom: png | jpeg
	JPEGQuality   float64  `json:"jpegQuality"`   // custom, jpeg
}

func defaultModelOptions() modelOptions {
	return modelOptions{Out: defaultOut("full"), Format: "obj", Detail: "full", Ordering: "sequential", Sensitivity: "normal", Masking: true, Mesh: "triangle",
		Checkpoint: true, Gravity: true, MaxPolygons: 250000, TextureDim: "8k", TextureMaps: []string{"all"}, TextureFormat: "png", JPEGQuality: 0.8}
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// check says what's wrong with the options, if anything.
func (o modelOptions) check() error {
	switch {
	case !cleanName(o.Out) || strings.ContainsAny(o.Out, `:*?"<>|`):
		return errors.New("the output folder: one folder name, no slashes, no leading dot, nor any of : * ? \" < > |")
	case o.Out == buildDir || o.Out == sampleSub:
		return fmt.Errorf("the output folder can't be %s: that's the build's own (or the sample shots')", o.Out)
	case !oneOf(o.Format, "obj", "usdz"):
		return fmt.Errorf("format %q?", o.Format)
	case !oneOf(o.Detail, "preview", "reduced", "medium", "full", "raw", "custom"):
		return fmt.Errorf("detail %q?", o.Detail)
	case !oneOf(o.Ordering, "sequential", "unordered"):
		return fmt.Errorf("ordering %q?", o.Ordering)
	case !oneOf(o.Sensitivity, "normal", "high"):
		return fmt.Errorf("sensitivity %q?", o.Sensitivity)
	case !oneOf(o.Mesh, "triangle", "quad"):
		return fmt.Errorf("mesh %q?", o.Mesh)
	}
	if o.Detail == "custom" {
		switch {
		case o.MaxPolygons < 1:
			return errors.New("the most polygons must be 1 or more")
		case !oneOf(o.TextureDim, "1k", "2k", "4k", "8k", "16k"):
			return fmt.Errorf("texture size %q?", o.TextureDim)
		case len(o.TextureMaps) == 0:
			return errors.New("pick at least one texture map")
		case !oneOf(o.TextureFormat, "png", "jpeg"):
			return fmt.Errorf("texture format %q?", o.TextureFormat)
		case o.JPEGQuality < 0 || o.JPEGQuality > 1:
			return errors.New("JPEG quality must be 0–1")
		}
		for _, m := range o.TextureMaps {
			if !oneOf(m, "diffuse", "normal", "roughness", "displacement", "ao", "all") {
				return fmt.Errorf("texture map %q?", m)
			}
		}
	}
	return nil
}

// args are the tool's flags for the options (the folder and out added by
// the caller).
func (o modelOptions) args() []string {
	a := []string{"--format", o.Format, "--detail", o.Detail, "--ordering", o.Ordering, "--sensitivity", o.Sensitivity, "--mesh", o.Mesh}
	if !o.Masking {
		a = append(a, "--no-masking")
	}
	if o.IgnoreBBox {
		a = append(a, "--ignore-bbox")
	}
	if !o.Checkpoint {
		a = append(a, "--no-checkpoint")
	}
	if !o.Gravity {
		a = append(a, "--no-gravity")
	}
	if o.Bounds {
		a = append(a, "--bounds")
	}
	if o.PointCloud {
		a = append(a, "--pointcloud")
	}
	if o.Detail == "custom" {
		a = append(a, "--max-polygons", fmt.Sprint(o.MaxPolygons), "--texture-dim", o.TextureDim,
			"--texture-maps", strings.Join(o.TextureMaps, ","), "--texture-format", o.TextureFormat)
		if o.TextureFormat == "jpeg" {
			a = append(a, "--jpeg-quality", fmt.Sprint(o.JPEGQuality))
		}
	}
	return a
}

// modelState is a build, as the pages see it (type "model"): sent as it goes.
type modelState struct {
	Type      string            `json:"type"`   // "model"
	Folder    string            `json:"folder"` // the capture's name
	Out       string            `json:"out"`    // the model's folder, in the capture's
	Running   bool              `json:"running"`
	Fraction  float64           `json:"fraction"`  // the model's progress, 0–1
	Stage     string            `json:"stage"`     // what it's doing (preProcessing, imageAlignment, …)
	Remaining float64           `json:"remaining"` // s left, as it reckons (0: not known)
	Photos    map[string]string `json:"photos"`    // file → loaded | invalid | skipped | aligned | unaligned
	Reasons   map[string]string `json:"reasons"`   // file → why it's invalid
	Notes     []string          `json:"notes"`     // e.g. downsampled, stitching incomplete
	Result    string            `json:"result"`    // the model's path, done
	Error     string            `json:"error"`
	Outcome   string            `json:"outcome"` // done | failed | cancelled ("" running)
	Started   int64             `json:"started"` // ms since 1970
	Ended     int64             `json:"ended"`
}

// modelBuilder runs the builds.
type modelBuilder struct {
	out func(any)

	mu    sync.Mutex
	state modelState
	cmd   *exec.Cmd
	sent  time.Time // (progress sent at most a few times a second)
}

func (b *modelBuilder) msg() modelState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.copyState()
}

// copyState is the state, its maps copied (b.mu held).
func (b *modelBuilder) copyState() modelState {
	s := b.state
	s.Type = "model"
	s.Photos, s.Reasons = map[string]string{}, map[string]string{}
	for k, v := range b.state.Photos {
		s.Photos[k] = v
	}
	for k, v := range b.state.Reasons {
		s.Reasons[k] = v
	}
	s.Notes = append([]string(nil), b.state.Notes...)
	return s
}

func (b *modelBuilder) send() {
	b.mu.Lock()
	s := b.copyState()
	b.sent = time.Now()
	b.mu.Unlock()
	b.out(s)
}

// objCapTool finds apple-obj-cap: on the PATH, beside gosts-rig, or built in
// the source tree (cmd/apple-obj-cap, from where gosts-rig runs: go run ./cmd/gosts-rig),
// building it there if it's not built or its source is newer.
func objCapTool() (string, error) {
	if p, err := exec.LookPath("apple-obj-cap"); err == nil {
		return p, nil
	}
	if exe, err := os.Executable(); err == nil {
		if p := filepath.Join(filepath.Dir(exe), "apple-obj-cap"); fileExists(p) {
			return p, nil
		}
	}
	src := filepath.Join("cmd", "apple-obj-cap", "main.swift")
	bin := filepath.Join("cmd", "apple-obj-cap", "apple-obj-cap")
	ss, err := os.Stat(src)
	if err != nil {
		return "", errors.New("apple-obj-cap isn't built (cmd/apple-obj-cap: swiftc -O -parse-as-library -swift-version 5 main.swift -o apple-obj-cap), nor on the PATH")
	}
	if bs, err := os.Stat(bin); err == nil && !bs.ModTime().Before(ss.ModTime()) {
		return filepath.Abs(bin)
	}
	log.Printf("model: building apple-obj-cap")
	out, err := exec.Command("swiftc", "-O", "-parse-as-library", "-swift-version", "5", src, "-o", bin).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("building apple-obj-cap: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return filepath.Abs(bin)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// start builds a model from capture folder name with o: config.json saved
// first, then the tool run, its events followed.
func (b *modelBuilder) start(a *app, name string, o modelOptions) error {
	if !cleanName(name) || strings.HasSuffix(name, "-samples") {
		return errors.New("no such capture")
	}
	dir := filepath.Join(photoDir, name)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		if name == currentBatch() { // (the capture going: its folder made with its first file)
			return errors.New("no photo files saved in this capture yet: nothing to build from")
		}
		return errors.New("no such capture")
	}
	if o.Out == "" {
		o.Out = defaultOut(o.Detail)
	}
	if err := o.check(); err != nil {
		return err
	}
	if currentBatch() == name && a.cap.isRunning() {
		return errors.New("a capture is saving into it: build its model once the capture is done")
	}
	b.mu.Lock()
	if b.state.Running {
		b.mu.Unlock()
		return fmt.Errorf("a model is being built already (%s)", b.state.Folder)
	}
	b.mu.Unlock()
	out, work := filepath.Join(dir, o.Out), filepath.Join(dir, buildDir)
	// Object Capture writes only into an empty folder: one with something
	// in it goes to the Trash first, if it's this capture's model from
	// before (the output folder its last build used); else it's not ours to
	// clear: another name, please.
	clear := false
	if ents, err := os.ReadDir(out); err == nil && len(ents) > 0 {
		last := defaultModelOptions()
		if data, err := os.ReadFile(filepath.Join(work, "config.json")); err == nil {
			json.Unmarshal(data, &last)
		}
		if last.Out != o.Out {
			return fmt.Errorf("%s has files in it (not a model built here): pick another output folder, or empty it", o.Out)
		}
		clear = true
	}
	tool, err := objCapTool()
	if err != nil {
		return err
	}
	if clear {
		if err := trash(out); err != nil {
			return fmt.Errorf("the model from before (%s) couldn't go to the Trash: %w", o.Out, err)
		}
		log.Printf("model: %s's model from before (%s) moved to the Trash", name, o.Out)
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	if data, err := json.MarshalIndent(o, "", "  "); err == nil {
		os.WriteFile(filepath.Join(work, "config.json"), data, 0o644)
	}
	os.Remove(filepath.Join(work, "status.json")) // (the last build's)
	cmd := exec.Command(tool, append([]string{dir, "--out", out, "--work", work}, o.args()...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("apple-obj-cap: %w", err)
	}
	b.mu.Lock()
	b.cmd = cmd
	b.state = modelState{Folder: name, Out: o.Out, Running: true, Photos: map[string]string{}, Reasons: map[string]string{}, Started: time.Now().UnixMilli()}
	b.mu.Unlock()
	log.Printf("model: building %s (%s)", name, strings.Join(o.args(), " "))
	b.send()
	go b.follow(cmd, stdout, &stderr, work)
	return nil
}

// event is one of the tool's (see apple-obj-cap's emit).
type objCapEvent struct {
	Event     string   `json:"event"`
	File      string   `json:"file"`
	Files     []string `json:"files"`
	Reason    string   `json:"reason"`
	Request   string   `json:"request"`
	Fraction  *float64 `json:"fraction"`
	Stage     string   `json:"stage"`
	Remaining *float64 `json:"remaining"`
	Path      string   `json:"path"`
	Message   string   `json:"message"`
	OK        *bool    `json:"ok"`
}

// follow reads the tool's events into the state till it ends.
func (b *modelBuilder) follow(cmd *exec.Cmd, stdout interface{ Read([]byte) (int, error) }, stderr *strings.Builder, work string) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024) // (aligned: every photo's name on a line)
	for sc.Scan() {
		var e objCapEvent
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		b.mu.Lock()
		s := &b.state
		urgent := true // (progress: sent a few times a second; the rest at once)
		switch e.Event {
		case "start":
			for _, f := range e.Files {
				s.Photos[f] = "waiting"
			}
		case "loaded":
			if s.Photos[e.File] != "invalid" {
				s.Photos[e.File] = "loaded"
			}
		case "invalid":
			s.Photos[e.File], s.Reasons[e.File] = "invalid", e.Reason
		case "skipped":
			s.Photos[e.File] = "skipped"
		case "aligned":
			used := map[string]bool{}
			for _, f := range e.Files {
				used[f] = true
			}
			for f, st := range s.Photos {
				switch {
				case used[f]:
					s.Photos[f] = "aligned"
				case st == "loaded" || st == "waiting":
					s.Photos[f] = "unaligned"
				}
			}
		case "progress":
			if e.Request == "model" {
				if e.Fraction != nil {
					s.Fraction = *e.Fraction
				}
				if e.Stage != "" {
					s.Stage = e.Stage
				}
				if e.Remaining != nil {
					s.Remaining = *e.Remaining
				}
			}
			urgent = time.Since(b.sent) > 250*time.Millisecond
		case "downsampling":
			s.Notes = append(s.Notes, "The photos were scaled down to fit (memory).")
		case "stitchingIncomplete":
			s.Notes = append(s.Notes, "Not all the photos could be joined into one model.")
		case "result":
			if e.Request == "model" {
				s.Result, s.Fraction = e.Path, 1
			}
		case "error":
			if s.Error == "" {
				s.Error = e.Message
			}
		case "cancelled":
			s.Outcome = "cancelled"
		case "done":
			if e.OK != nil && *e.OK {
				s.Outcome = "done"
			}
		}
		b.mu.Unlock()
		if urgent {
			b.send()
		}
	}
	err := cmd.Wait()
	b.mu.Lock()
	s := &b.state
	s.Running, s.Ended, b.cmd = false, time.Now().UnixMilli(), nil
	if s.Outcome == "" {
		s.Outcome = "failed"
		if s.Error == "" {
			s.Error = strings.TrimSpace(stderr.String())
			if s.Error == "" && err != nil {
				s.Error = err.Error()
			}
		}
	}
	if s.Outcome == "cancelled" {
		s.Error = "" // (the requests cut short report errors: not ones)
	}
	if s.Outcome != "done" { // (photos still waiting: not reached)
		for f, st := range s.Photos {
			if st == "loaded" || st == "waiting" {
				s.Photos[f] = "waiting"
			}
		}
	}
	saved := b.copyState()
	b.mu.Unlock()
	if data, err := json.MarshalIndent(saved, "", "  "); err == nil {
		os.WriteFile(filepath.Join(work, "status.json"), data, 0o644)
	}
	log.Printf("model: %s %s", saved.Folder, saved.Outcome)
	b.send()
}

// cancel stops the build going on (the tool cancels its session, cleanly).
func (b *modelBuilder) cancel() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cmd == nil || b.cmd.Process == nil {
		return errors.New("no model is being built")
	}
	return b.cmd.Process.Signal(syscall.SIGINT)
}

// modelInfo is a capture's model folder for the page (GET
// /batches/{name}/model): the options last used (else the defaults), and
// how the last build went (if it's not the one going on).
type modelInfo struct {
	Options modelOptions `json:"options"`
	Last    *modelState  `json:"last"`
	Gravity bool         `json:"gravity"` // the capture has the rig's gravity.json
}

func (a *app) handleModelInfo(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !cleanName(name) {
		http.NotFound(w, r)
		return
	}
	dir := filepath.Join(photoDir, name)
	info := modelInfo{Options: defaultModelOptions(), Gravity: fileExists(filepath.Join(dir, "gravity.json"))}
	if data, err := os.ReadFile(filepath.Join(dir, buildDir, "config.json")); err == nil {
		json.Unmarshal(data, &info.Options)
	}
	if data, err := os.ReadFile(filepath.Join(dir, buildDir, "status.json")); err == nil {
		var s modelState
		if json.Unmarshal(data, &s) == nil {
			info.Last = &s
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}
