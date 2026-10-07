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
	"strconv"
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

	mu        sync.Mutex
	fire, dl  time.Duration // recently: firing a photo, downloading its JPEG
	apertures [2]int        // the lens's f-stops, as indexes of the camera's choices (see Probe; [1] 0: not probed)
}

// gphoto2Req is one thing for the worker: a photo to take, or (development)
// shell lines to run.
type gphoto2Req struct {
	ctx     context.Context
	fired   chan error    // the shutter's fired (or not)
	shutter               // its callbacks (see shutter)
	lines   []string      // instead: run these, reply on out (then errc)
	await   *gphoto2Await // instead: wait for a setting to take, reply on errc
	out     chan []string
	errc    chan error
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
	tmp, err := os.MkdirTemp("", "gosts-rig-camera-*")
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
func (g *gphoto2Camera) Shoot(ctx context.Context, sh shutter) error {
	r := gphoto2Req{ctx: ctx, fired: make(chan error, 1), shutter: sh}
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

// gphoto2Await is a setting to wait for: path to read value.
type gphoto2Await struct {
	path, value string
	timeout     time.Duration
}

// awaitSetting waits (up to timeout) for the camera to show path at value.
func (g *gphoto2Camera) awaitSetting(path, value string, timeout time.Duration) error {
	r := gphoto2Req{await: &gphoto2Await{path, value, timeout}, errc: make(chan error, 1)}
	select {
	case g.reqs <- r:
	case <-g.done:
		return errors.New("the camera is closed")
	}
	return <-r.errc
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

// cameraFileNumber is the number in a camera's file name (capt_DSC01116.JPG:
// 1116), 0 if there's none.
func cameraFileNumber(name string) int {
	m := gphoto2FileNumber.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	k, _ := strconv.Atoi(m[1])
	return k
}

var gphoto2FileNumber = regexp.MustCompile(`(\d+)\.[A-Za-z0-9]+$`)

// gphoto2Added is a "FILEADDED name folder" line: a file the camera wrote.
var gphoto2Added = regexp.MustCompile(`FILEADDED (\S+)`)

// pictureWait is how long a photo waits for its JPEG before it's given up
// (e.g. the camera on RAW only: nothing comes over).
var pictureWait = 30 * time.Second

// heldWait is how long a file that may be a given-up photo's, late, waits
// for the next number to show it was (see gaveUp in work).
var heldWait = 3 * time.Second

// work owns the shell: photos first, then collecting pictures.
func (g *gphoto2Camera) work(sh *gphoto2Shell, tmp string) {
	defer close(g.done)
	defer os.RemoveAll(tmp)
	defer sh.close()
	// Each photo is matched to its JPEG by the camera's file number
	// (capt_DSC01116.JPG): the camera numbers its files in turn, so a photo
	// fired expects the next number. A file numbered lower is a leftover
	// (from an earlier session, or a photo that gave up waiting): kept, but
	// no photo's picture. One numbered higher means the camera skipped
	// (a photo taken on the camera itself): the numbers waited for move up.
	type waiting struct {
		got   func(photo, error)
		since time.Time
		dir   string // its batch's folder (as the shutter went)
		num   int    // the camera's number its files will have (0: not known yet)
	}
	var queue []waiting  // photos taken whose JPEG hasn't come yet, oldest first
	next := 0            // the number the next photo fired will get (0: not known yet)
	saw := func(k int) { // a file numbered k is on the camera: new photos come after it
		next = max(next, k+1)
		for _, w := range queue {
			next = max(next, w.num+1)
		}
	}
	keep := func(name string, data []byte, why string) { // a file that's no photo's picture
		if path, err := saveUnique(batchFolder(), name, data); err == nil {
			log.Printf("camera: %s, saved %s", why, path)
		}
	}
	// deliver gives queue[i] its picture.
	deliver := func(i int, name string, data []byte) {
		w := queue[i]
		queue = append(queue[:i:i], queue[i+1:]...)
		p := photo{JPEG: data, At: time.Now()}
		if path, err := saveUnique(w.dir, name, data); err == nil {
			p.Files = []string{path}
		} else {
			log.Printf("camera: saving %s: %v", name, err)
		}
		w.got(p, nil)
	}
	// A photo that gave up may never have been taken: the camera can refuse
	// to fire (focus first, and it found none). Then the next photo gets the
	// number it was given, and its file looks like the given-up one's, late.
	// So that file is held a moment (heldWait): if the next number comes, it
	// was late after all (a leftover); if not, it's the oldest waiting
	// photo's, and the numbers waited for move down one.
	gaveUp := map[int]bool{} // the numbers photos had when they gave up
	type heldFile struct {
		name string
		data []byte
		k    int
		at   time.Time
	}
	var held *heldFile
	settle := func(k int) { // a file numbered k is on the camera: a held one before it was late
		if held != nil && k > held.k {
			keep(held.name, held.data, fmt.Sprintf("%s came after its photo gave up", held.name))
			held = nil
		}
	}
	resolve := func(now bool) { // a held file waited long enough (or now: its photo's giving up): nothing came after it
		if held == nil || (!now && time.Since(held.at) < heldWait) {
			return
		}
		h := held
		held = nil
		if len(queue) == 0 || queue[0].num != h.k+1 {
			keep(h.name, h.data, fmt.Sprintf("%s isn't any waiting photo's", h.name))
			return
		}
		log.Printf("camera: the photo numbered %d wasn't taken (the camera refused?): %s is the next one's", h.k, h.name)
		for j := range queue {
			queue[j].num--
		}
		next--
		deliver(0, h.name, h.data)
	}
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
				k := cameraFileNumber(m[1])
				if k > 0 {
					settle(k)
				}
				if k > 0 && len(queue) == 0 {
					saw(k) // a photo nobody's waiting for (taken on the camera): later ones come after it
				}
				continue
			}
			m := gphoto2Saved.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			name := m[1]
			data, rerr := os.ReadFile(filepath.Join(tmp, name))
			os.Remove(filepath.Join(tmp, name))
			if rerr != nil {
				log.Printf("camera: %s: %v", name, rerr)
				continue
			}
			k := cameraFileNumber(name)
			if k > 0 {
				settle(k)
			}
			if !strings.EqualFold(filepath.Ext(name), ".jpg") && !strings.EqualFold(filepath.Ext(name), ".jpeg") {
				// Not a JPEG (a RAW-only camera, or --keep-raw not honoured):
				// kept, but no photo's picture by itself.
				keep(name, data, "not a JPEG")
				continue
			}
			// Whose is it? The photo waiting for its number; else, if it's past
			// the oldest waiting one's (or the numbers aren't known yet), the
			// oldest's, the numbers moving up; else a leftover.
			i := -1
			if k > 0 {
				for j := range queue {
					if queue[j].num == k {
						i = j
					}
				}
				if i < 0 && len(queue) > 0 && (queue[0].num == 0 || k > queue[0].num) {
					if queue[0].num == 0 { // the first number seen: those not numbered yet follow it
						for j := range queue {
							if queue[j].num == 0 {
								queue[j].num = k + j
							}
						}
					} else { // the camera skipped: all move up
						d := k - queue[0].num
						for j := range queue {
							queue[j].num += d
						}
					}
					i = 0
				}
			} else if len(queue) > 0 { // no number in its name: in turn
				i = 0
			}
			if k > 0 {
				saw(k)
			}
			if i < 0 && k > 0 && gaveUp[k] && len(queue) > 0 && queue[0].num == k+1 && held == nil {
				held = &heldFile{name, data, k, time.Now()} // late, or the next photo's (see gaveUp)
				continue
			}
			if i < 0 { // a leftover
				keep(name, data, fmt.Sprintf("%s isn't any waiting photo's", name))
				continue
			}
			deliver(i, name, data)
		}
		if err != nil {
			log.Printf("camera: %v", err)
		}
		resolve(false)
		for len(queue) > 0 && time.Since(queue[0].since) > pictureWait {
			if held != nil && queue[0].num == held.k+1 {
				resolve(true) // it's had its picture all along
				continue
			}
			if queue[0].num > 0 {
				gaveUp[queue[0].num] = true
			}
			queue[0].got(photo{}, errors.New("its picture didn't come over (did the camera fire? it may wait for focus; is it on RAW & JPEG?)"))
			queue = queue[1:]
		}
	}
	// First, whatever the camera still has to hand over (photos from before:
	// they mustn't be taken for new ones). It also tells the numbers so far.
	collect(1500 * time.Millisecond)
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
		if a := r.await; a != nil {
			// The camera applies a setting a moment later, telling it in its
			// events: let them come (collecting any pictures meanwhile, as
			// ever), and look again, till it shows or time's up.
			deadline := time.Now().Add(a.timeout)
			var err error
			for {
				collect(250 * time.Millisecond)
				out, rerr := sh.run("get-config "+a.path, 10*time.Second)
				c, perr := parseGphoto2Config(out)
				if rerr == nil && perr == nil && c.current == a.value {
					err = nil
					break
				}
				if time.Now().After(deadline) {
					err = fmt.Errorf("the camera stayed at %q", c.current)
					if rerr != nil || perr != nil {
						err = errors.Join(rerr, perr)
					}
					break
				}
			}
			r.errc <- err
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
		queue = append(queue, waiting{got: r.got, since: time.Now(), dir: r.folder(), num: next})
		if next > 0 {
			next++
		}
		r.fired <- nil
	}
	if held != nil { // closing: whoever's it was, it's kept
		keep(held.name, held.data, fmt.Sprintf("%s isn't any waiting photo's", held.name))
	}
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
