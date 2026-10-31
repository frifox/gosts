package main

import (
	"context"
	"embed"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// web holds the page and the 3D engine it uses (three.js, MIT licence),
// embedded so the rig PC needs no internet.
//
//go:embed web
var webFiles embed.FS

func logPrint(msg string) { log.Print(msg) }

// app ties the rig, the capture runner and the browser windows together.
type app struct {
	cfg    *configFile
	rig    *rig
	cap    *capture
	camera *cameraConn
	model  *modelBuilder
	flash  flashMeasure // measuring the flash's limit (see flash.go)

	mu      sync.Mutex
	clients map[*client]struct{}
	quit    func()      // gosts-rig quits (cleanly: see main), as for Ctrl-C
	restart atomic.Bool // quitting to start again (Settings' Restart: see main)
	net     listening   // where the pages are served (see listen)
	// onMotion, if set, is told the move speed and acceleration changed
	// here (Servo Settings' Control card shows them too).
	onMotion func()
}

type client struct {
	conn *websocket.Conn
	send chan any
}

func (c *client) push(msg any) {
	select {
	case c.send <- msg:
	default: // too slow: drop
	}
}

func (a *app) broadcast(msg any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for c := range a.clients {
		c.push(msg)
	}
}

// request is a command from the browser; only the fields for Type are set.
type request struct {
	Seq       int           `json:"seq"`
	Type      string        `json:"type"`
	Port      string        `json:"port"`
	Baud      int           `json:"baud"`
	Elevation *float64      `json:"elevation"`
	Folder    string        `json:"folder"` // modelStart: the capture
	Model     *modelOptions `json:"model"`  // modelStart: apple-obj-cap's options
	Azimuth   *float64      `json:"azimuth"`
	On        bool          `json:"on"`
	Axis      string        `json:"axis"` // torque: "elevation", "azimuth", or "" (all)
	Roles     *Roles        `json:"roles"`
	Motion    *Motion       `json:"motion"`
	Plan      *Plan         `json:"plan"`
	Cooldown  *Cooldown     `json:"cooldown"`
	Camera    string        `json:"camera"`
	N         int           `json:"n"`
	Lines     []string      `json:"lines"`
	Key       string        `json:"key"`
	Value     string        `json:"value"`
}

type resultMsg struct {
	Type  string `json:"type"` // "result"
	Seq   int    `json:"seq"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

func (a *app) exec(req request) (any, error) {
	switch req.Type {
	case "exit", "restart": // Settings' Quit: gosts-rig quits, once the page has its answer; Restart: and starts again (main)
		if req.Type == "restart" {
			a.restart.Store(true)
			log.Print("restart: asked from the page: quitting to start again")
		} else {
			log.Print("exit: asked from the page: quitting")
		}
		if a.model.msg().Running {
			a.model.cancel() // (Object Capture runs apart: it'd go on after)
		}
		a.stopMeasure()
		go func() { time.Sleep(200 * time.Millisecond); a.quit() }()
		return nil, nil
	case "ports":
		return listPorts()
	case "connect":
		if err := a.rig.connect(context.Background(), req.Port, req.Baud); err != nil {
			return nil, err
		}
		if req.Port == simPort {
			return nil, nil // don't make the simulator the default
		}
		return nil, a.cfg.update(func(c *Config) { c.Port, c.Baud = req.Port, req.Baud })
	case "disconnect":
		a.cap.stop()
		a.rig.disconnect()
		return nil, nil
	case "scan":
		return nil, a.rig.scan(context.Background())
	case "roles":
		if req.Roles == nil {
			return nil, errors.New("no roles")
		}
		return nil, a.rig.setRoles(*req.Roles)
	case "motion":
		if req.Motion == nil {
			return nil, errors.New("no motion settings")
		}
		m := *req.Motion
		// 0 would mean the servos' maximum (speed) and no ramp at all
		// (acceleration): never what a 0 typed here should do.
		if m.Speed < 50 || m.Speed > 3400 {
			return nil, errors.New("speed must be 5–298°/s")
		}
		if m.SpeedBy != SpeedByPhoto && m.SpeedBy != SpeedByRate {
			m.SpeedBy = SpeedByPhoto // (a page from before: the seconds a photo)
		}
		if m.SecPerPhoto < 0.3 || m.SecPerPhoto > 120 {
			return nil, errors.New("seconds a photo must be 0.3–120")
		}
		if m.Acc < 1 || m.Acc > 254 {
			return nil, errors.New("acceleration must be 1–254 (×100 step/s²)")
		}
		saved := a.cfg.get().Motion
		if m.AzimuthSpeed == 0 { // (a page from before: one speed)
			m.AzimuthSpeed = saved.AzimuthSpeed
		}
		if m.AzimuthAcc == 0 {
			m.AzimuthAcc = saved.AzimuthAcc
		}
		if m.AzimuthSpeed < 50 || m.AzimuthSpeed > 3400 {
			return nil, errors.New("azimuth speed must be 5–298°/s")
		}
		if m.AzimuthAcc < 1 || m.AzimuthAcc > 254 {
			return nil, errors.New("azimuth acceleration must be 1–254 (×100 step/s²)")
		}
		if m.ElevationMin < -90 || m.ElevationMax > 90 || m.ElevationMin >= m.ElevationMax {
			return nil, errors.New("elevation limits must be within -90°…90°, min below max")
		}
		err := a.cfg.update(func(c *Config) { c.Motion = m })
		if err == nil {
			_ = a.cap.preview(a.cfg.get().Plan) // the plan follows the elevation range
		}
		a.rig.sendState()
		if a.onMotion != nil {
			a.onMotion()
		}
		return nil, err
	case "move":
		return nil, a.rig.moveTo(req.Elevation, req.Azimuth)
	case "stop":
		a.cap.stop()
		return nil, a.rig.stop()
	case "torque":
		return nil, a.rig.torque(req.On, req.Axis)
	case "cooldown": // pausing a capture now and then (see Cooldown): applies from the next photo
		if req.Cooldown == nil {
			return nil, errors.New("no cooldown")
		}
		cd := *req.Cooldown
		if cd.Shots < 1 || cd.Minutes <= 0 || cd.Minutes > 120 {
			return nil, errors.New("pause after 1 or more shots, for more than 0 and up to 120 minutes")
		}
		if cd.DarkMinutes <= 0 || cd.DarkMinutes > 120 {
			return nil, errors.New("pause after a no-flash photo for more than 0 and up to 120 minutes")
		}
		if cd.On && cd.Dark {
			return nil, errors.New("one way of pausing at a time")
		}
		was := a.cfg.get().Cooldown
		if err := a.cfg.update(func(c *Config) { c.Cooldown = cd }); err != nil {
			return nil, err
		}
		if how := cd.how(); how != was.how() && a.cap.isRunning() { // (a capture going: said, so it's seen in its log)
			a.rig.logf("info", "capture: the flash pause changed to %s (from %s), from the next photo", how, was.how())
		}
		a.rig.sendState()
		a.cap.send() // the estimate
		return nil, nil
	case "plan": // save and show a plan without running it
		if req.Plan == nil {
			return nil, errors.New("no plan")
		}
		req.Plan.Path = req.Plan.path()
		if err := a.cfg.update(func(c *Config) { c.Plan = *req.Plan }); err != nil {
			return nil, err
		}
		a.rig.sendState() // windows follow the plan's mode
		return nil, a.cap.preview(*req.Plan)
	case "public": // Settings' Public: served to the network (On), or this Mac alone
		if err := a.setPublic(req.On); err != nil {
			return nil, err
		}
		return a.appSettings(), nil
	case "appSettings": // the header's gear: the folders
		return a.appSettings(), nil
	case "showFolder": // Settings' folder icons: the captures' (Key "photos") or the settings' folder, in the Finder
		return nil, a.showFolder(req.Key)
	case "chooseFolder": // macOS' folder chooser: prompt Key, starting at Value ("" back: canceled)
		return chooseFolder(req.Key, req.Value)
	case "setPhotos": // the captures' folder: Value
		if err := a.setPhotos(req.Value); err != nil {
			return nil, err
		}
		return a.appSettings(), nil
	case "setSettingsDir": // the settings' folder (config.toml): Value
		if err := a.setSettingsDir(req.Value); err != nil {
			return nil, err
		}
		return a.appSettings(), nil
	case "flashMeasure": // Camera Settings' Measure: the flash's limit at power Key
		return nil, a.measureFlash(req.Key)
	case "flashMeasureStop":
		a.stopMeasure()
		return nil, nil
	case "start":
		if a.flash.running() {
			return nil, errors.New("the flash is being measured: stop that first")
		}
		if req.Plan == nil {
			return nil, errors.New("no plan")
		}
		req.Plan.Path = req.Plan.path()
		if err := a.cfg.update(func(c *Config) { c.Plan = *req.Plan }); err != nil {
			return nil, err
		}
		return nil, a.cap.start(*req.Plan)
	case "pause":
		return nil, a.cap.pause(req.On)
	case "cameras":
		return a.camera.camerasNow(), nil
	case "cameraConnect":
		if err := a.camera.connect(req.Camera); err != nil {
			return nil, err
		}
		if req.Camera == simCameraID {
			return nil, nil // don't make the simulator the default
		}
		return nil, a.cfg.update(func(c *Config) { c.Camera = req.Camera })
	case "shoot": // Take Photo
		if a.cap.msg().Running {
			return nil, errors.New("a capture is running")
		}
		return nil, a.camera.shoot(context.Background(), true, nil)
	case "cameraSettings": // the Config dialog's settings, from the camera
		return a.cameraSettings()
	case "cameraSet": // one setting, then all of them again
		sc, err := a.settingsCam()
		if err != nil {
			return nil, err
		}
		if req.Key == "zoom" && a.cap.msg().Running {
			return nil, errors.New("a capture is running: zooming would change its photos' focal length")
		}
		if err := sc.Set(req.Key, req.Value); err != nil {
			return nil, err
		}
		return a.cameraSettings()
	case "cameraProbe": // the F-stop sweep: first the f-stops the lens really has
		if a.cap.msg().Running {
			return nil, errors.New("a capture is running")
		}
		sc, err := a.settingsCam()
		if err != nil {
			return nil, err
		}
		pc, ok := sc.(proberCamera)
		if !ok {
			return nil, errors.New("this camera can't be probed")
		}
		if err := pc.Probe(req.Key); err != nil {
			return nil, err
		}
		return a.cameraSettings()
	case "cameraExclude": // the F-stop sweep: one it couldn't take a photo at, off the menu
		sc, err := a.settingsCam()
		if err != nil {
			return nil, err
		}
		if pc, ok := sc.(proberCamera); ok {
			pc.Exclude(req.Key, req.Value)
		}
		return a.cameraSettings()
	case "autofocus": // Camera Settings' Focus: focus once (and stay, in Manual): how it went
		fc, err := a.focusCam()
		if err != nil {
			return nil, err
		}
		return fc.Autofocus()
	case "focusNudge": // Focus: drive the lens N steps (−: near)
		fc, err := a.focusCam()
		if err != nil {
			return nil, err
		}
		return nil, fc.Nudge(req.N)
	case "measurePace": // how often the camera keeps up taking photos (a few sample shots, timed)
		if a.cap.msg().Running {
			return nil, errors.New("a capture is running")
		}
		pace, err := a.camera.measurePace(context.Background())
		a.cap.send() // the estimate follows the pace
		return pace, err
	case "sampleShot": // Config: a photo to judge the settings by
		if a.cap.msg().Running {
			return nil, errors.New("a capture is running")
		}
		if a.flash.running() {
			return nil, errors.New("the flash is being measured")
		}
		return nil, a.camera.sample(context.Background())
	case "shootBurst": // testing: N photos queued at once, as Moving Shots takes them
		for i := 0; i < req.N; i++ {
			if err := a.camera.shoot(context.Background(), false, nil); err != nil {
				return nil, err
			}
		}
		return nil, nil
	case "gphoto2Shell": // development: run a gphoto2 shell script on the connected camera
		return gphoto2ShellScript(a.camera, req.Lines)
	case "diag": // read-only: the role servos' raw registers, for debugging
		return a.rig.diag()
	case "photoDelete": // one photo off the timeline
		a.camera.delete(req.N)
		return nil, nil
	case "modelStart": // Build Model (Browse): a capture's model, with Object Capture
		if req.Model == nil {
			return nil, errors.New("no model options")
		}
		return nil, a.model.start(a, req.Folder, *req.Model)
	case "modelCancel":
		return nil, a.model.cancel()
	case "photosReset": // New Capture: the timeline cleared, a new folder
		if a.cap.isRunning() {
			return nil, errors.New("a capture is running")
		}
		folder := a.camera.reset(true)
		a.cap.forget() // (an interrupted one: left as it is, in its folder)
		return map[string]string{"folder": folder}, nil
	case "openCapture": // Browse: a capture opened (an interrupted one: to resume; else to add photos to)
		return nil, a.adopt(req.Folder)
	case "cameraDisconnect":
		a.cap.stop()
		a.camera.disconnect()
		return nil, nil
	}
	return nil, errors.New("unknown command " + req.Type)
}

// upgrader takes the pages' own websockets (and a program's, with no
// Origin), not another site's: a page elsewhere, open in a browser here or
// (Public) on the network, can't drive the rig.
var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}}

func (a *app) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{conn: conn, send: make(chan any, 256)}
	a.mu.Lock()
	a.clients[c] = struct{}{}
	a.mu.Unlock()
	c.push(a.rig.state())
	c.push(a.cap.msg())
	c.push(a.camera.msg())
	c.push(a.camera.timeline())
	c.push(a.camera.batchStatus())
	c.push(a.model.msg())
	a.flash.mu.Lock()
	if a.flash.last.Type != "" {
		c.push(a.flash.last)
	}
	a.flash.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer conn.Close()
		for {
			select {
			case msg := <-c.send:
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := conn.WriteJSON(msg); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()
	defer func() {
		a.mu.Lock()
		delete(a.clients, c)
		a.mu.Unlock()
		close(done)
	}()
	// The camera's requests in turn, but beside the others: a camera not
	// answering holds up only its own (and Disconnect skips them all).
	camQ := make(chan func(), 64)
	defer close(camQ)
	go func() {
		for f := range camQ {
			f()
		}
	}()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req request
		if err := json.Unmarshal(data, &req); err != nil {
			c.push(resultMsg{Type: "result", Seq: req.Seq, Error: "bad request: " + err.Error()})
			continue
		}
		run := func() {
			data, err := a.exec(req)
			res := resultMsg{Type: "result", Seq: req.Seq, OK: err == nil, Data: data}
			if err != nil {
				res.Error = err.Error()
			}
			c.push(res)
		}
		switch {
		case req.Type == "connect" || req.Type == "scan" || req.Type == "disconnect" || req.Type == "cameraDisconnect" || req.Type == "chooseFolder": // slow, or must not wait (the folder chooser: open till picked): keep the window responsive
			go run()
		case cameraRequests[req.Type]:
			select {
			case camQ <- run:
			default: // a long queue: the camera's stuck; it won't make this one later
				go run()
			}
		default:
			run()
		}
	}
}

// cameraRequests are the requests that talk to the camera (in turn, beside
// the rest: see handleWS).
var cameraRequests = map[string]bool{
	"cameras": true, "cameraConnect": true, "shoot": true, "cameraSettings": true, "cameraSet": true, "cameraProbe": true, "cameraExclude": true,
	"autofocus": true, "focusNudge": true, "measurePace": true, "sampleShot": true, "shootBurst": true, "gphoto2Shell": true,
}

// handlePhoto serves a photo on the timeline (/photo/{n}.jpg; ?thumb, its
// thumbnail).
func (a *app) handlePhoto(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(strings.TrimSuffix(r.PathValue("file"), ".jpg"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var jpg []byte
	var ok bool
	if r.URL.Query().Has("thumb") { // ?thumb: a small one, for the timeline
		jpg, ok = a.camera.thumb(n)
	} else {
		var p photo
		p, ok = a.camera.pictureOf(n)
		jpg = p.JPEG
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(jpg)
}

// handlePreview serves a live-view frame (GET /camera/preview): a JPEG from
// a real camera; for the simulated one, what its settings do to the picture
// (JSON: the page renders the frame itself, as for its photos).
func (a *app) handlePreview(w http.ResponseWriter, r *http.Request) {
	if a.cap.msg().Running {
		http.Error(w, "a capture is running", http.StatusConflict)
		return
	}
	a.camera.mu.Lock()
	cam := a.camera.cam
	a.camera.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	switch c := cam.(type) {
	case nil:
		http.Error(w, errNoCamera.Error(), http.StatusNotFound)
	case *simCamera:
		settings := map[string]string{}
		if ss, err := c.Settings(); err == nil {
			for _, st := range ss {
				settings[st.Key] = st.Current
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"sim": true, "exposure": c.exposure(), "blur": c.blur(), "settings": settings})
	case previewCamera:
		jpg, err := c.Preview()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(jpg)
	default:
		http.Error(w, "this camera has no live view", http.StatusNotImplemented)
	}
}

// handleSimPhoto takes a simulated photo's picture, rendered by a page
// (POST /photo/sim?n=, a JPEG).
func (a *app) handleSimPhoto(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST a JPEG", http.StatusMethodNotAllowed)
		return
	}
	n, err := strconv.Atoi(r.URL.Query().Get("n"))
	if err != nil {
		http.Error(w, "n: photo number", http.StatusBadRequest)
		return
	}
	jpg, err := io.ReadAll(io.LimitReader(r.Body, 20<<20))
	if err != nil || len(jpg) == 0 {
		http.Error(w, "no picture", http.StatusBadRequest)
		return
	}
	a.camera.simPicture(n, jpg)
}

// settingsCam is the connected camera, if its settings can be changed.
func (a *app) settingsCam() (settingsCamera, error) {
	a.camera.mu.Lock()
	cam := a.camera.cam
	a.camera.mu.Unlock()
	if cam == nil {
		return nil, errNoCamera
	}
	sc, ok := cam.(settingsCamera)
	if !ok {
		return nil, errors.New("this camera's settings can't be changed from here")
	}
	return sc, nil
}

func (a *app) cameraSettings() ([]cameraSetting, error) {
	sc, err := a.settingsCam()
	if err != nil {
		return nil, err
	}
	ss, err := sc.Settings()
	if err == nil {
		a.camera.noteSettings(ss)
	}
	_, focus := sc.(focusCamera)
	for i := range ss {
		if ss[i].Key == "focus" && focus {
			ss[i].FocusModes = focusModes
		}
	}
	return ss, err
}

// focusCam is the camera, if its focus can be set from here.
func (a *app) focusCam() (focusCamera, error) {
	if a.cap.msg().Running {
		return nil, errors.New("a capture is running")
	}
	a.camera.mu.Lock()
	cam := a.camera.cam
	a.camera.mu.Unlock()
	if cam == nil {
		return nil, errNoCamera
	}
	fc, ok := cam.(focusCamera)
	if !ok {
		return nil, errors.New("this camera's focus can't be set from here")
	}
	return fc, nil
}
