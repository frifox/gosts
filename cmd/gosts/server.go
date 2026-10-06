package main

import (
	"context"
	"embed"
	_ "embed"
	"encoding/json"
	"errors"
	"log"
	"net/http"
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
	cfg *configFile
	rig *rig
	cap *capture

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
		if m.Speed < 0 || m.Speed > 3400 || m.Acc < 0 || m.Acc > 254 || m.ElevationMin >= m.ElevationMax {
			return nil, errors.New("invalid motion settings")
		}
		err := a.cfg.update(func(c *Config) { c.Motion = m })
		if err == nil {
			_ = a.cap.preview(a.cfg.get().Plan) // the plan follows the elevation range
		}
		if err == nil && m.MultiTurn {
			a.rig.mu.Lock()
			found := append([]uint8(nil), a.rig.found...)
			a.rig.mu.Unlock()
			a.rig.ensureMultiTurn(found)
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
		if err := a.cap.preview(*req.Plan); err != nil {
			return nil, err
		}
		return nil, a.cfg.update(func(c *Config) { c.Plan = *req.Plan })
	case "start":
		if req.Plan == nil {
			return nil, errors.New("no plan")
		}
		if err := a.cfg.update(func(c *Config) { c.Plan = *req.Plan }); err != nil {
			return nil, err
		}
		return nil, a.cap.start(*req.Plan)
	case "pause":
		return nil, a.cap.pause(req.On)
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
