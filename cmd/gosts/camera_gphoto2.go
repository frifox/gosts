package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// gphoto2Prefix marks a camera id as one gphoto2 found on the USB bus; the
// rest of the id is the USB port gphoto2 addresses it by (e.g.
// "usb:020,005"), so a specific camera can still be picked if more than one
// is attached.
const gphoto2Prefix = "gphoto2:"

// gphoto2Device is one camera gphoto2 sees attached.
type gphoto2Device struct {
	model, port string
}

// gphoto2PortLine matches one "<model>   <port>" line from
// "gphoto2 --auto-detect"; the header and the "----" separator above it
// don't end in a usb: port, so they're skipped without special-casing them.
var gphoto2PortLine = regexp.MustCompile(`^(.*\S)\s+(usb:\d+,\d+)\s*$`)

// gphoto2Detect lists the cameras gphoto2 currently sees attached over USB.
// Empty, without error, if gphoto2 isn't installed or nothing answers —
// callers treat "no real camera" as the normal case.
func gphoto2Detect() []gphoto2Device {
	out, err := exec.Command("gphoto2", "--auto-detect").Output()
	if err != nil {
		return nil
	}
	var devs []gphoto2Device
	for _, line := range strings.Split(string(out), "\n") {
		if m := gphoto2PortLine.FindStringSubmatch(line); m != nil {
			devs = append(devs, gphoto2Device{model: m[1], port: m[2]})
		}
	}
	return devs
}

// gphoto2Camera drives a real camera (the Sony A6600, first) through one
// gphoto2 shell kept open (opening the camera for every photo took most of
// the time). A worker goroutine owns the shell: it fires the shutter for each
// photo asked for (trigger-capture: about 1.1 s on the A6600, nothing
// downloaded), and in between collects what the camera has ready
// (wait-event-and-download, a short while at a time), handing each JPEG to
// the photo it belongs to, in order. Set the camera to RAW & JPEG with
// --keep-raw in force: only the JPEG comes over USB, the RAW stays on the
// card (its name is logged).
type gphoto2Camera struct {
	port string
	reqs chan gphoto2Req
	done chan struct{} // closed when the worker has ended

	mu       sync.Mutex
	fire, dl time.Duration // recently: firing a photo, downloading its JPEG
}

// gphoto2Req is one thing for the worker: a photo to take, or (development)
// shell lines to run.
type gphoto2Req struct {
	ctx    context.Context
	fired  chan error         // the shutter's fired (or not)
	firing func()             // just before the shutter
	got    func(photo, error) // the photo's picture, later
	lines  []string           // instead: run these, reply on out (then errc)
	out    chan []string
	errc   chan error
}

// openGphoto2Camera connects to the camera gphoto2 sees on port (failing if
// it's no longer there: unplugged, or woken from sleep since the page last
// listed cameras), opening its shell.
func openGphoto2Camera(port string) (Camera, string, error) {
	model := ""
	for _, d := range gphoto2Detect() {
		if d.port == port {
			model = d.model
		}
	}
	if model == "" {
		return nil, "", fmt.Errorf("no camera on %s (unplugged?)", port)
	}
	// Cautious until measured (the A6600: about 1.1 s and 0.7 s).
	g := &gphoto2Camera{port: port, reqs: make(chan gphoto2Req, 64), done: make(chan struct{}), fire: 1200 * time.Millisecond, dl: 800 * time.Millisecond}
	tmp, err := os.MkdirTemp("", "gosts-camera-*")
	if err != nil {
		return nil, "", err
	}
	sh, err := startGphoto2Shell(port, tmp, "--keep-raw", "--force-overwrite", "--filename", "%f.%C")
	if err != nil {
		os.RemoveAll(tmp)
		return nil, "", fmt.Errorf("gphoto2: %w", err)
	}
	go g.work(sh, tmp)
	return g, model, nil
}

// Shoot asks for a photo and returns once the shutter has fired; got is
// called with its picture once it has come over (or with an error).
func (g *gphoto2Camera) Shoot(ctx context.Context, firing func(), got func(photo, error)) error {
	r := gphoto2Req{ctx: ctx, fired: make(chan error, 1), firing: firing, got: got}
	select {
	case g.reqs <- r:
	case <-g.done:
		return errors.New("the camera is closed")
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-r.fired:
		return err
	case <-g.done:
		return errors.New("the camera is closed")
	}
}

// MinInterval is about how soon after one photo the next can be taken, for
// as long as it keeps up: firing it, and downloading its JPEG in between
// (the worker does both), with a margin.
func (g *gphoto2Camera) MinInterval() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.fire + g.dl + 200*time.Millisecond
}

// measured blends a new measurement into a recent average.
func measured(avg *time.Duration, d time.Duration, mu *sync.Mutex) {
	mu.Lock()
	*avg = (*avg*3 + d) / 4
	mu.Unlock()
}

// Close ends the shell once the photos taken so far have come over (giving
// up after a while).
func (g *gphoto2Camera) Close() error {
	select {
	case <-g.done:
		return nil
	default:
	}
	close(g.reqs)
	select {
	case <-g.done:
	case <-time.After(20 * time.Second):
	}
	return nil
}

// runLines runs shell lines on the camera's shell, in turn with the photos,
// and returns what each printed.
func (g *gphoto2Camera) runLines(lines []string) ([]string, error) {
	r := gphoto2Req{lines: lines, out: make(chan []string, 1), errc: make(chan error, 1)}
	select {
	case g.reqs <- r:
	case <-g.done:
		return nil, errors.New("the camera is closed")
	}
	return <-r.out, <-r.errc
}

// script runs shell lines on the camera's shell (development) and returns
// what each printed, with how long it took.
func (g *gphoto2Camera) script(lines []string) (string, error) {
	var b strings.Builder
	for _, l := range lines {
		start := time.Now()
		outs, err := g.runLines([]string{l})
		out := ""
		if len(outs) > 0 {
			out = outs[0]
		}
		fmt.Fprintf(&b, "> %s  [%v]\n%s", l, time.Since(start).Round(time.Millisecond), out)
		if err != nil {
			fmt.Fprintf(&b, "ERROR: %v\n", err)
			break
		}
	}
	return b.String(), nil
}

// gphoto2Saved is a "Saving file as …" line: a file downloaded.
var gphoto2Saved = regexp.MustCompile(`Saving file as (\S+)`)

// gphoto2Added is a "FILEADDED name folder" line: a file the camera wrote.
var gphoto2Added = regexp.MustCompile(`FILEADDED (\S+)`)

// pictureWait is how long a photo waits for its JPEG before it's given up
// (e.g. the camera on RAW only: nothing comes over).
const pictureWait = 30 * time.Second

// work owns the shell: photos first, then collecting pictures.
func (g *gphoto2Camera) work(sh *gphoto2Shell, tmp string) {
	defer close(g.done)
	defer os.RemoveAll(tmp)
	defer sh.close()
	type waiting struct {
		got   func(photo, error)
		since time.Time
	}
	var queue []waiting // photos taken whose JPEG hasn't come yet, oldest first
	open := true
	collect := func(d time.Duration) {
		start := time.Now()
		out, err := sh.run(fmt.Sprintf("wait-event-and-download %dms", d.Milliseconds()), d+10*time.Second)
		if n := len(gphoto2Saved.FindAllString(out, -1)); n > 0 {
			measured(&g.dl, time.Since(start)/time.Duration(n), &g.mu)
		}
		for _, l := range strings.Split(out, "\n") {
			if m := gphoto2Added.FindStringSubmatch(l); m != nil {
				log.Printf("camera: on the card: %s", m[1])
			}
			m := gphoto2Saved.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			name := m[1]
			data, rerr := os.ReadFile(filepath.Join(tmp, name))
			os.Remove(filepath.Join(tmp, name))
			if !strings.EqualFold(filepath.Ext(name), ".jpg") && !strings.EqualFold(filepath.Ext(name), ".jpeg") {
				// Not a JPEG (a RAW-only camera, or --keep-raw not honoured):
				// kept, but it's no photo's picture by itself.
				if rerr == nil {
					if path, err := saveUnique(photoDay(), name, data); err == nil {
						log.Printf("camera: saved %s", path)
					}
				}
				continue
			}
			if len(queue) == 0 {
				log.Printf("camera: %s came over, but no photo was waiting for it", name)
				continue
			}
			w := queue[0]
			queue = queue[1:]
			if rerr != nil {
				w.got(photo{}, rerr)
				continue
			}
			p := photo{JPEG: data, At: time.Now()}
			if path, err := saveUnique(photoDay(), name, data); err == nil {
				p.Files = []string{path}
			} else {
				log.Printf("camera: saving %s: %v", name, err)
			}
			w.got(p, nil)
		}
		if err != nil {
			log.Printf("camera: %v", err)
		}
		for len(queue) > 0 && time.Since(queue[0].since) > pictureWait {
			queue[0].got(photo{}, errors.New("its picture didn't come over (is the camera on RAW & JPEG?)"))
			queue = queue[1:]
		}
	}
	for open || len(queue) > 0 {
		var r gphoto2Req
		var ok bool
		if len(queue) > 0 || !open {
			select { // a photo or script asked for: right away; else collect a bit
			case r, ok = <-g.reqs:
				if !ok {
					open = false
				}
			default:
				collect(300 * time.Millisecond)
				continue
			}
		} else {
			r, ok = <-g.reqs
			if !ok {
				open = false
				continue
			}
		}
		if !ok {
			continue
		}
		if r.lines != nil {
			var outs []string
			var err error
			for _, l := range r.lines {
				var out string
				out, err = sh.run(l, 60*time.Second)
				outs = append(outs, out)
				if err != nil {
					break
				}
			}
			r.out <- outs
			r.errc <- err
			continue
		}
		if r.ctx.Err() != nil { // the capture stopped meanwhile
			r.fired <- r.ctx.Err()
			continue
		}
		r.firing()
		start := time.Now()
		out, err := sh.run("trigger-capture", 15*time.Second)
		if err == nil && strings.Contains(out, "*** Error") {
			err = errors.New(gphoto2Error([]byte(out)))
		}
		if err != nil {
			r.fired <- fmt.Errorf("gphoto2: %w", err)
			continue
		}
		measured(&g.fire, time.Since(start), &g.mu)
		queue = append(queue, waiting{got: r.got, since: time.Now()})
		r.fired <- nil
	}
}

// photoDay is the photo folder for today (created if needed).
func photoDay() string {
	dir := filepath.Join(photoDir, time.Now().Format("2006-01-02"))
	os.MkdirAll(dir, 0o755)
	return dir
}

// saveUnique writes data to dir/name, or name with -2, -3… before the
// extension if that's taken (the camera's numbering restarts on a new card).
func saveUnique(dir, name string, data []byte) (string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		p := filepath.Join(dir, name)
		if i > 1 {
			p = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, i, ext))
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return p, err
	}
}

// gphoto2Error is the gist of gphoto2's error output (without its "send us
// a debug log" boilerplate), with what to do for the usual macOS one.
func gphoto2Error(out []byte) string {
	s := string(out)
	if strings.Contains(s, "Could not claim the USB device") {
		return "another program has the camera (on macOS: quit Photos and Image Capture, then run: killall ptpcamerad mscamerad-xpc)"
	}
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "For debugging messages") {
			break // the rest is how to report a bug
		}
		if l != "" && l != "*** Error ***" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, " ")
}

// gphoto2ShellScript runs lines in the connected camera's gphoto2 shell (for
// development: finding out what the camera does), one at a time, and returns
// what each printed and how long it took.
func gphoto2ShellScript(c *cameraConn, lines []string) (string, error) {
	c.mu.Lock()
	g, ok := c.cam.(*gphoto2Camera)
	c.mu.Unlock()
	if !ok {
		return "", errors.New("no gphoto2 camera connected")
	}
	return g.script(lines)
}

// gphoto2Shell is a running "gphoto2 --shell": one session with the camera
// kept open, commands sent one at a time.
type gphoto2Shell struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	lines chan string // what it prints, a line at a time
	buf   strings.Builder
}

// gphoto2Prompt ends what the shell prints for a command.
var gphoto2Prompt = regexp.MustCompile(`gphoto2: \{[^}]*\} [^>]*> $`)

func startGphoto2Shell(port, dir string, args ...string) (*gphoto2Shell, error) {
	cmd := exec.Command("gphoto2", append([]string{"--port", port}, append(args, "--shell")...)...)
	cmd.Dir = dir
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	sh := &gphoto2Shell{cmd: cmd, in: in, lines: make(chan string, 256)}
	go func() { // chunks as they come (the prompt has no newline)
		r := bufio.NewReader(out)
		chunk := make([]byte, 4096)
		for {
			n, err := r.Read(chunk)
			if n > 0 {
				sh.lines <- string(chunk[:n])
			}
			if err != nil {
				close(sh.lines)
				return
			}
		}
	}()
	if _, err := sh.wait(30 * time.Second); err != nil { // the first prompt
		sh.close()
		return nil, err
	}
	return sh, nil
}

// wait collects output until the prompt (or timeout, or the shell ending).
func (sh *gphoto2Shell) wait(timeout time.Duration) (string, error) {
	deadline := time.After(timeout)
	for {
		if gphoto2Prompt.MatchString(sh.buf.String()) {
			out := gphoto2Prompt.ReplaceAllString(sh.buf.String(), "")
			sh.buf.Reset()
			return out, nil
		}
		select {
		case chunk, ok := <-sh.lines:
			if !ok {
				out := sh.buf.String()
				sh.buf.Reset()
				return out, errors.New("gphoto2 ended")
			}
			sh.buf.WriteString(chunk)
		case <-deadline:
			out := sh.buf.String()
			sh.buf.Reset()
			return out, errors.New("gphoto2 didn't answer in time")
		}
	}
}

// run sends one command and returns what it printed.
func (sh *gphoto2Shell) run(line string, timeout time.Duration) (string, error) {
	if _, err := io.WriteString(sh.in, line+"\n"); err != nil {
		return "", err
	}
	out, err := sh.wait(timeout)
	// The shell echoes the command first.
	out = strings.TrimPrefix(strings.TrimPrefix(out, line+"\r\n"), line+"\n")
	return out, err
}

func (sh *gphoto2Shell) close() {
	io.WriteString(sh.in, "exit\n")
	sh.in.Close()
	done := make(chan struct{})
	go func() { sh.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		sh.cmd.Process.Kill()
		<-done
	}
}
