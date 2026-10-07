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
	"strconv"
	"strings"
	"sync"
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

	mu      sync.Mutex
	clients map[*client]struct{}
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
	Seq       int      `json:"seq"`
	Type      string   `json:"type"`
	Port      string   `json:"port"`
	Baud      int      `json:"baud"`
	Elevation *float64 `json:"elevation"`
	Azimuth   *float64 `json:"azimuth"`
	On        bool     `json:"on"`
	Roles     *Roles   `json:"roles"`
	Motion    *Motion  `json:"motion"`
	Plan      *Plan    `json:"plan"`
	Rig       *Rig     `json:"rig"`
	Camera    string   `json:"camera"`
	N         int      `json:"n"`
	Lines     []string `json:"lines"`
	Key       string   `json:"key"`
	Value     string   `json:"value"`
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
		if m.Acc < 1 || m.Acc > 254 {
			return nil, errors.New("acceleration must be 1–254 (×100 step/s²)")
		}
		if m.ElevationMin < -90 || m.ElevationMax > 90 || m.ElevationMin >= m.ElevationMax {
			return nil, errors.New("elevation limits must be within -90°…90°, min below max")
		}
		err := a.cfg.update(func(c *Config) { c.Motion = m })
		if err == nil {
			_ = a.cap.preview(a.cfg.get().Plan) // the plan follows the elevation range
		}
		a.rig.sendState()
		return nil, err
	case "move":
		return nil, a.rig.moveTo(req.Elevation, req.Azimuth)
	case "stop":
		a.cap.stop()
		return nil, a.rig.stop()
	case "torque":
		return nil, a.rig.torque(req.On)
	case "plan": // save and show a plan without running it
		if req.Plan == nil {
			return nil, errors.New("no plan")
		}
		if err := a.cfg.update(func(c *Config) { c.Plan = *req.Plan }); err != nil {
			return nil, err
		}
		a.rig.sendState() // windows follow the plan's mode
		return nil, a.cap.preview(*req.Plan)
	case "start":
		if req.Plan == nil {
			return nil, errors.New("no plan")
		}
		if err := a.cfg.update(func(c *Config) { c.Plan = *req.Plan }); err != nil {
			return nil, err
		}
		return nil, a.cap.start(*req.Plan)
	case "rig": // measurements for the 3D view
		if req.Rig == nil {
			return nil, errors.New("no measurements")
		}
		if err := req.Rig.check(); err != nil {
			return nil, err
		}
		err := a.cfg.update(func(c *Config) { c.Rig = *req.Rig })
		a.rig.sendState()
		return nil, err
	case "pause":
		return nil, a.cap.pause(req.On)
	case "cameras":
		return listCameras(), nil
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
		if err := sc.Set(req.Key, req.Value); err != nil {
			return nil, err
		}
		return a.cameraSettings()
	case "sampleShot": // Config: a photo to judge the settings by
		if a.cap.msg().Running {
			return nil, errors.New("a capture is running")
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
	case "photosReset": // Reset: clear the timeline
		a.camera.reset()
		return nil, nil
	case "cameraDisconnect":
		a.cap.stop()
		a.camera.disconnect()
		return nil, nil
	}
	return nil, errors.New("unknown command " + req.Type)
}

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }} // meant for localhost

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
		if req.Type == "connect" || req.Type == "scan" { // slow: keep the window responsive
			go run()
		} else {
			run()
		}
	}
}

// handlePhoto serves a photo on the timeline (/photo/{n}.jpg).
func (a *app) handlePhoto(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(strings.TrimSuffix(r.PathValue("file"), ".jpg"))
	p, ok := a.camera.photo(n)
	if err != nil || !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(p.JPEG)
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
	return sc.Settings()
}
