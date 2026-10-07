package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Camera takes the photos. Drivers for real cameras (Sony A6600 over USB
// first) implement it; the simulated camera stands in for one so the rest
// can be tried without a camera.
type Camera interface {
	// Shoot takes one photo and returns once it is taken (the rig may move
	// on then; the photo itself can still be on its way).
	Shoot(ctx context.Context) error
	Close() error
}

// cameraInfo is a camera that can be connected, for the page's list.
type cameraInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// simCameraID selects the simulated camera.
const simCameraID = "sim"

// listCameras lists the cameras that can be connected.
func listCameras() []cameraInfo {
	return []cameraInfo{{ID: simCameraID, Name: "Simulator", Detail: "Simulated camera: takes each photo after a short shutter lag, saves nothing"}}
}

func openCamera(id string) (Camera, string, error) {
	switch id {
	case simCameraID:
		return &simCamera{lag: 30 * time.Millisecond}, "Simulator", nil
	}
	return nil, "", fmt.Errorf("no camera %q", id)
}

// simCamera "takes" a photo after a shutter lag.
type simCamera struct {
	lag time.Duration
	mu  sync.Mutex
	n   int
}

func (s *simCamera) Shoot(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.lag):
	}
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
	return nil
}

func (s *simCamera) Close() error { return nil }

// cameraConn is the connected camera, if any.
type cameraConn struct {
	out func(any)

	mu   sync.Mutex
	cam  Camera
	id   string
	name string
}

// cameraMsg tells the page which camera is connected.
type cameraMsg struct {
	Type      string `json:"type"` // "camera"
	Connected bool   `json:"connected"`
	ID        string `json:"id"`
	Name      string `json:"name"`
}

func (c *cameraConn) msg() cameraMsg {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cameraMsg{Type: "camera", Connected: c.cam != nil, ID: c.id, Name: c.name}
}

func (c *cameraConn) connect(id string) error {
	cam, name, err := openCamera(id)
	if err != nil {
		return err
	}
	c.mu.Lock()
	old := c.cam
	c.cam, c.id, c.name = cam, id, name
	c.mu.Unlock()
	if old != nil {
		old.Close()
	}
	c.out(c.msg())
	return nil
}

func (c *cameraConn) disconnect() {
	c.mu.Lock()
	old := c.cam
	c.cam, c.id, c.name = nil, "", ""
	c.mu.Unlock()
	if old != nil {
		old.Close()
	}
	c.out(c.msg())
}

func (c *cameraConn) connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cam != nil
}

var errNoCamera = errors.New("no camera connected")

// shoot takes a photo with the connected camera.
func (c *cameraConn) shoot(ctx context.Context) error {
	c.mu.Lock()
	cam := c.cam
	c.mu.Unlock()
	if cam == nil {
		return errNoCamera
	}
	return cam.Shoot(ctx)
}
