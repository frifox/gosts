package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A camera on a Raspberry Pi (the Arducam OwlSight 64MP on a Pi 5): the Pi
// runs gosts-cam (cmd/gosts-cam), which owns the camera and answers HTTP.
// The Pi's USB-C port is in USB gadget mode, so the Mac sees it as a USB
// network adapter: one cable, as with the A6600.

// piPrefix marks a camera id as a Pi's; the rest is its host (and port).
const piPrefix = "pi:"

// piPort is gosts-cam's port.
const piPort = "8090"

// piHosts are where a Pi plugged in over USB answers: rpi-usb-gadget's own
// address when it hands out the addresses, else its name (gosts-cam's
// install.sh names it so), as when the Mac shares its connection.
var piHosts = []string{"10.12.194.1", "gosts-cam.local"}

// piConfigHost is one more place to look (Config.PiCameraHost), first.
var piConfigHost string

// piProbeWait is how long listing waits for a Pi to answer.
var piProbeWait = 400 * time.Millisecond

// piProbeClient asks without a proxy (a Pi's on the cable, not out there).
var piProbeClient = &http.Client{Transport: &http.Transport{}}

// piInfo is what gosts-cam says about itself (GET /info).
type piInfo struct {
	Model   string   `json:"model"`
	Sensor  string   `json:"sensor"`
	Version string   `json:"version"`
	Modes   [][2]int `json:"modes"`
}

// piAddr is host with gosts-cam's port, if it has none.
func piAddr(host string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(host, piPort)
}

// piDetect lists the Pis answering now, each once (its address and name both
// answering: the first of them kept).
func piDetect() []cameraInfo {
	hosts := piHosts
	if piConfigHost != "" {
		hosts = append([]string{piConfigHost}, hosts...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), piProbeWait)
	defer cancel()
	type found struct {
		info  piInfo
		addrs []string // what its host resolves to (to spot the same Pi twice)
		ok    bool
	}
	res := make([]found, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var info piInfo
			if err := piGetJSON(ctx, piProbeClient, piAddr(h), "/info", &info); err != nil {
				return
			}
			hn, _, _ := net.SplitHostPort(piAddr(h))
			addrs, _ := net.DefaultResolver.LookupHost(ctx, hn)
			res[i] = found{info, addrs, true}
		}()
	}
	wg.Wait()
	var cams []cameraInfo
	var seen []string
	for i, f := range res {
		if !f.ok || slices.ContainsFunc(f.addrs, func(a string) bool { return slices.Contains(seen, a) }) {
			continue
		}
		seen = append(seen, f.addrs...)
		name := f.info.Model
		if name == "" {
			name = "Raspberry Pi camera"
		}
		cams = append(cams, cameraInfo{ID: piPrefix + hosts[i], Name: name, Detail: "USB, Raspberry Pi at " + hosts[i]})
	}
	return cams
}

// piCamera drives gosts-cam. A photo: POST /shoot answers a line as the
// frame's taken (the rig may move on), then one once its files are saved
// on the Pi; a worker fetches them (in turn, so the photos come in order),
// saves them in the photo's folder, and deletes them from the Pi.
type piCamera struct {
	addr string
	ctl  *http.Client // settings, focus, preview, shooting
	get  *http.Client // fetching files (a 64 MP DNG is over 100 MB, USB 2.0 ~30 MB/s)

	ctx    context.Context // the camera's: cancelled by Close once it's waited
	cancel context.CancelFunc

	shootMu sync.Mutex     // one photo at a time
	pending chan piPending // fired, files to fetch: the worker's
	quit    chan struct{}  // closed by Close: the worker finishes what's pending
	done    chan struct{}  // closed when the worker has ended
	closeMu sync.Once

	mu         sync.Mutex
	fire, save time.Duration // recently: asking to its frame taken; then to its files saved on the Pi
	dl         time.Duration // and fetching them
}

// piPending is a photo taken whose files are still to come.
type piPending struct {
	body   io.ReadCloser
	lines  *bufio.Reader
	folder string
	got    func(photo, error) // nil: it gave up (the files are kept, no photo's)
	fired  time.Time
}

// piEvent is a line of POST /shoot's answer.
type piEvent struct {
	Event string         `json:"event"` // fired, saved, error
	At    time.Time      `json:"at"`
	Files []string       `json:"files"`
	Meta  map[string]any `json:"meta"`
	Error string         `json:"error"`
}

func openPiCamera(host string) (Camera, string, error) {
	addr := piAddr(host)
	ctx, cancel := context.WithCancel(context.Background())
	p := &piCamera{
		addr: addr, ctx: ctx, cancel: cancel,
		// No proxy (it's on the cable); a shot's answer runs till its files
		// are saved, so no overall timeout for it (contexts end things), only
		// for an answer to start.
		ctl:     &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 30 * time.Second}},
		get:     &http.Client{Transport: &http.Transport{}, Timeout: 2 * time.Minute},
		pending: make(chan piPending, 64),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
		// Cautious till measured.
		fire: 500 * time.Millisecond, save: time.Second, dl: time.Second,
	}
	var info piInfo
	ictx, icancel := context.WithTimeout(ctx, 5*time.Second)
	defer icancel()
	if err := piGetJSON(ictx, p.ctl, addr, "/info", &info); err != nil {
		cancel()
		return nil, "", fmt.Errorf("no camera at %s (unplugged, or gosts-cam not running?): %w", host, err)
	}
	go p.work()
	name := info.Model
	if name == "" {
		name = "Raspberry Pi camera"
	}
	return p, name, nil
}

// Shoot asks gosts-cam for a photo, returning once its frame is taken.
func (p *piCamera) Shoot(ctx context.Context, sh shutter) error {
	p.shootMu.Lock()
	defer p.shootMu.Unlock()
	select {
	case <-p.quit:
		return errCameraClosed
	default:
	}
	folder := ""
	if sh.folder != nil {
		folder = sh.folder()
	}
	if sh.firing != nil {
		sh.firing()
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(p.ctx, http.MethodPost, "http://"+p.addr+"/shoot", nil)
	if err != nil {
		return err
	}
	type first struct {
		ev   piEvent
		resp *http.Response
		br   *bufio.Reader
		err  error
	}
	ch := make(chan first, 1)
	go func() {
		resp, err := p.ctl.Do(req)
		if err != nil {
			ch <- first{err: err}
			return
		}
		if resp.StatusCode != http.StatusOK {
			ch <- first{err: piHTTPError(resp)}
			return
		}
		br := bufio.NewReader(resp.Body)
		ev, err := piReadEvent(br)
		ch <- first{ev, resp, br, err}
	}()
	var f first
	select {
	case f = <-ch:
	case <-ctx.Done():
		// Gave up waiting; it may still be taken: its files are kept when
		// they come, as no photo's.
		go func() {
			if f := <-ch; f.err == nil && f.ev.Event == "fired" {
				p.queue(piPending{body: f.resp.Body, lines: f.br, folder: folder, fired: time.Now()})
			} else if f.resp != nil {
				f.resp.Body.Close()
			}
		}()
		return ctx.Err()
	}
	if f.err != nil {
		return fmt.Errorf("camera: %w", f.err)
	}
	switch f.ev.Event {
	case "fired":
	case "error":
		f.resp.Body.Close()
		return fmt.Errorf("camera: %s", f.ev.Error)
	default:
		f.resp.Body.Close()
		return fmt.Errorf("camera: %q, not fired", f.ev.Event)
	}
	measured(&p.fire, time.Since(start), &p.mu)
	at := f.ev.At
	if at.IsZero() {
		at = time.Now()
	}
	p.queue(piPending{body: f.resp.Body, lines: f.br, folder: folder, got: sh.got, fired: at})
	return nil
}

// queue hands a photo taken to the worker (or, closing, drops it).
func (p *piCamera) queue(w piPending) {
	select {
	case p.pending <- w:
	case <-p.done:
		w.body.Close()
		if w.got != nil {
			w.got(photo{}, errCameraClosed)
		}
	}
}

// work fetches each photo's files, in turn.
func (p *piCamera) work() {
	defer close(p.done)
	for {
		select {
		case w := <-p.pending:
			p.fetch(w)
		case <-p.quit:
			for { // what's pending, then done
				select {
				case w := <-p.pending:
					p.fetch(w)
				default:
					return
				}
			}
		}
	}
}

// fetch waits for a photo's files to be saved on the Pi, then fetches them.
func (p *piCamera) fetch(w piPending) {
	defer w.body.Close()
	fail := func(err error) {
		log.Printf("camera: %v", err)
		if w.got != nil {
			w.got(photo{}, err)
		}
	}
	saveStart := time.Now()
	ev, err := piReadEvent(w.lines)
	if err != nil {
		fail(fmt.Errorf("photo's files: %w", err))
		return
	}
	if ev.Event == "error" {
		fail(errors.New(ev.Error))
		return
	}
	if ev.Event != "saved" || len(ev.Files) == 0 {
		fail(fmt.Errorf("photo's files: %q, no files", ev.Event))
		return
	}
	measured(&p.save, time.Since(saveStart), &p.mu)
	dlStart := time.Now()
	ph := photo{At: w.fired}
	folder := w.folder
	if folder == "" {
		folder = batchFolder()
	}
	for i, name := range ev.Files {
		name = path.Base(name)
		var keep *bytes.Buffer
		if i == 0 && piIsJPEG(name) {
			keep = &bytes.Buffer{} // the picture, to show
		}
		file, err := p.download(name, folder, keep)
		if err != nil {
			if i == 0 {
				fail(fmt.Errorf("fetching %s: %w", name, err))
				return
			}
			log.Printf("camera: fetching %s: %v (left on the Pi)", name, err)
			continue
		}
		ph.Files = append(ph.Files, file)
		if keep != nil {
			ph.JPEG = keep.Bytes()
		}
		p.remove(name)
	}
	measured(&p.dl, time.Since(dlStart), &p.mu)
	if w.got == nil {
		log.Printf("camera: a photo given up came after all, saved %s", strings.Join(ph.Files, ", "))
		return
	}
	w.got(ph, nil)
}

// download saves the Pi's file name in folder (keeping a copy in keep, if
// given), returning where.
func (p *piCamera) download(name, folder string, keep *bytes.Buffer) (string, error) {
	req, err := http.NewRequestWithContext(p.ctx, http.MethodGet, "http://"+p.addr+"/files/"+url.PathEscape(name), nil)
	if err != nil {
		return "", err
	}
	resp, err := p.get.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", piHTTPError(resp)
	}
	f, where, err := createUnique(folder, name)
	if err != nil {
		return "", err
	}
	var src io.Reader = resp.Body
	if keep != nil {
		src = io.TeeReader(resp.Body, keep)
	}
	_, err = io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(where)
		return "", err
	}
	return where, nil
}

// remove deletes a file fetched from the Pi.
func (p *piCamera) remove(name string) {
	req, err := http.NewRequestWithContext(p.ctx, http.MethodDelete, "http://"+p.addr+"/files/"+url.PathEscape(name), nil)
	if err != nil {
		return
	}
	resp, err := p.get.Do(req)
	if err != nil {
		log.Printf("camera: deleting %s on the Pi: %v", name, err)
		return
	}
	resp.Body.Close()
}

func piIsJPEG(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return ext == ".jpg" || ext == ".jpeg"
}

// MinInterval is about how soon after one photo the next can be taken: the
// Pi takes and saves one at a time, while the Mac fetches the last.
func (p *piCamera) MinInterval() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return max(p.fire+p.save, p.dl) + 150*time.Millisecond
}

// Close lets the photos taken so far come over (giving up after a while).
func (p *piCamera) Close() error {
	p.closeMu.Do(func() { close(p.quit) })
	select {
	case <-p.done:
	case <-time.After(closeWait):
		log.Printf("camera: closing: photos still coming: dropped")
	}
	p.cancel()
	<-p.done
	return nil
}

// Preview is a frame of the live view.
func (p *piCamera) Preview() ([]byte, error) {
	ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+p.addr+"/preview", nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.ctl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, piHTTPError(resp)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 20<<20))
}

// Settings are gosts-cam's, as the Config dialog has them (it answers in
// cameraSetting's shape, with the same keys: focus, mode, shutter, iso,
// whitebalance, colortemp; and resolution, format).
func (p *piCamera) Settings() ([]cameraSetting, error) {
	ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
	defer cancel()
	var ss []cameraSetting
	if err := piGetJSON(ctx, p.ctl, p.addr, "/settings", &ss); err != nil {
		return nil, err
	}
	return ss, nil
}

func (p *piCamera) Set(key, value string) error {
	var out struct{}
	return p.post("/settings", map[string]string{"key": key, "value": value}, &out)
}

// Autofocus focuses once (gosts-cam goes back to Manual after, if it was).
func (p *piCamera) Autofocus() (string, error) {
	var r struct {
		OK     bool    `json:"ok"`
		Status string  `json:"status"`
		Lens   float64 `json:"lensPosition"`
	}
	if err := p.post("/focus/auto", nil, &r); err != nil {
		return "", err
	}
	if r.Status == "" {
		r.Status = map[bool]string{true: "Focus Locked", false: "No focus found"}[r.OK]
	}
	return r.Status, nil
}

// Nudge moves the lens by steps (gosts-cam: a quarter diopter each).
func (p *piCamera) Nudge(steps int) error {
	if err := checkNudge(steps); err != nil {
		return err
	}
	var out struct{}
	return p.post("/focus/nudge?steps="+strconv.Itoa(steps), nil, &out)
}

// post sends body (JSON, if any) to gosts-cam, decoding its answer (if any)
// into out.
func (p *piCamera) post(where string, body, out any) error {
	ctx, cancel := context.WithTimeout(p.ctx, 30*time.Second)
	defer cancel()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+p.addr+where, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.ctl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return piHTTPError(resp)
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return err
	}
	return json.Unmarshal(b, out)
}

func piGetJSON(ctx context.Context, c *http.Client, addr, where string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+where, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return piHTTPError(resp)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// piReadEvent reads POST /shoot's next line.
func piReadEvent(br *bufio.Reader) (piEvent, error) {
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var ev piEvent
			if jerr := json.Unmarshal(line, &ev); jerr != nil {
				return piEvent{}, fmt.Errorf("camera said %q: %w", bytes.TrimSpace(line), jerr)
			}
			return ev, nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return piEvent{}, err
		}
	}
}

// piHTTPError is gosts-cam's error answer: its {"error"}, else the status.
func piHTTPError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return errors.New(e.Error)
	}
	if s := strings.TrimSpace(string(b)); s != "" && len(s) < 200 {
		return fmt.Errorf("%s: %s", resp.Status, s)
	}
	return errors.New(resp.Status)
}
