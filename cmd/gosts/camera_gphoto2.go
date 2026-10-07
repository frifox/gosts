package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
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

// gphoto2Camera drives a real camera (the Sony A6600, first) through the
// gphoto2 CLI, which talks PTP over USB. Each shot is its own process: there
// is no session kept open on the camera between shots.
type gphoto2Camera struct {
	port string
}

// openGphoto2Camera connects to the camera gphoto2 sees on port (failing if
// it's no longer there: unplugged, or woken from sleep since the page last
// listed cameras).
func openGphoto2Camera(port string) (Camera, string, error) {
	for _, d := range gphoto2Detect() {
		if d.port == port {
			return &gphoto2Camera{port: port}, d.model, nil
		}
	}
	return nil, "", fmt.Errorf("no camera on %s (unplugged?)", port)
}

func (g *gphoto2Camera) Shoot(ctx context.Context) (photo, error) {
	tmp, err := os.MkdirTemp("", "gosts-photo-*")
	if err != nil {
		return photo{}, err
	}
	defer os.RemoveAll(tmp)

	// --capture-image-and-download triggers the shutter and downloads what
	// the camera wrote (%f.%C keeps each file its camera name and
	// extension). --keep-raw leaves a RAW on the camera's card, not
	// downloaded: with the camera on RAW & JPEG (and PC Remote saving to
	// PC+Camera) only the small JPEG comes over USB, which is much quicker,
	// and the RAWs wait on the card. A JPEG downloaded is deleted from the
	// card (it's saved here). A camera on RAW only gives nothing to download.
	cmd := exec.CommandContext(ctx, "gphoto2",
		"--port", g.port,
		"--keep-raw",
		"--force-overwrite",
		"--filename", filepath.Join(tmp, "%f.%C"),
		"--capture-image-and-download",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return photo{}, fmt.Errorf("gphoto2: %w: %s", err, gphoto2Error(out))
	}
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); strings.Contains(l, " on the camera") {
			log.Printf("camera: %s", l) // what was taken, and what stays on the card
		}
	}
	files, err := os.ReadDir(tmp)
	if err != nil || len(files) == 0 {
		return photo{}, errors.New("the photo is on the camera's card but no JPEG came: set the camera's File Format to RAW & JPEG")
	}

	// Keep every file in the photo folder, then show the camera's JPEG, or
	// else the preview JPEG embedded in the RAW.
	dir := filepath.Join(photoDir, time.Now().Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return photo{}, err
	}
	var jpg, raw []byte
	var saved []string
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(tmp, f.Name()))
		if err != nil {
			return photo{}, err
		}
		path, err := saveUnique(dir, f.Name(), data)
		if err != nil {
			return photo{}, err
		}
		saved = append(saved, path)
		switch strings.ToLower(filepath.Ext(f.Name())) {
		case ".jpg", ".jpeg":
			jpg = data
		default:
			raw = data
		}
	}
	if jpg == nil && raw != nil {
		if jpg, err = rawPreview(raw); err != nil {
			return photo{Files: saved, At: time.Now()}, nil // kept, but nothing to show
		}
	}
	return photo{JPEG: jpg, Files: saved, At: time.Now()}, nil
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

func (g *gphoto2Camera) Close() error { return nil }
