// Command gosts-rig drives a photogrammetry rig built on ST3215 servos: two
// servos on top of the posts tilt the camera frame (elevation) as one
// mirrored group, a third turns the platform under the object (azimuth).
// The web page plans a capture (rings of photos round the object) and shows
// the rig live.
//
// Set up and calibrate the servos with gosts-ctl first (IDs, zero, tuning,
// limits); gosts-rig and gosts-ctl can't use the serial port at the same time,
// but gosts-ctl's console is in gosts-rig too (Servo Ctl in the rig's settings,
// served at /ctl/), on the rig's board.
//
//	go install github.com/frifox/gosts/cmd/gosts-rig@latest
//
//	gosts-rig                                # pick the board and camera in the browser
//	gosts-rig --rig /dev/cu.usbmodem1101     # connect the rig's board on startup
//	gosts-rig --rig sim                      # simulated rig (servos 1, 2, 3)
//	gosts-rig --rig sim --camera sim         # and a simulated camera
//	gosts-rig --listen :9000                 # serve the page on another port
package main

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/frifox/gosts/cmd/gosts-ctl/console"
)

func main() {
	port := flag.String("rig", "", `the rig's board to connect on startup: its serial device, or "sim" for the simulated rig (default: Port in the config file)`)
	flag.StringVar(port, "port", "", "the same as --rig (its old name)")
	sim := flag.Bool("sim", false, "the same as --rig sim (its old name)")
	camera := flag.String("camera", "", `camera to connect on startup: its id (as the page lists it), or "sim" for the simulated camera (default: Camera in the config file)`)
	addr := flag.String("listen", "", "address to serve the page on, e.g. \":8081\", \"localhost:9000\" or just a port, 9000 (default: ListenAddr in the config file, \":8081\" if unset)")
	flag.StringVar(addr, "addr", "", "the same as --listen (its old name)")
	cfgPath := flag.String("config", defaultConfigPath(), "settings file, created if missing")
	ctlPath := flag.String("ctl-config", ctlConfigPath(), "the servo console's settings file (Servo Ctl: gosts-ctl's, shared with it), created on the first change")
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("config: %s", *cfgPath)
	c := cfg.get()
	if *addr == "" {
		*addr = c.ListenAddr
	}
	if _, err := strconv.Atoi(*addr); err == nil { // just a port
		*addr = ":" + *addr
	}
	if *sim {
		*port = simPort
	}
	if c.PhotoDir != "" {
		photoDir = c.PhotoDir
	}
	log.Printf("photos: %s", photoDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	a := &app{cfg: cfg, clients: map[*client]struct{}{}}
	a.rig = &rig{cfg: cfg, out: a.broadcast}
	a.camera = &cameraConn{out: a.broadcast, cfg: cfg}
	a.cap = &capture{rig: a.rig, camera: a.camera, out: a.broadcast}
	// The servo console (Servo Ctl): gosts-ctl's, on the rig's board.
	ctlCfg, warnings, err := console.LoadConfig(*ctlPath)
	if err != nil {
		log.Fatal(err)
	}
	for _, w := range warnings {
		log.Print(w)
	}
	ctl := console.New(ctlCfg, nil, 50*time.Millisecond, false)
	a.rig.attach, a.rig.detach = ctl.Attach, ctl.Detach
	ctl.OnChange = func() { a.rig.rolesFromGroups(ctl.Groups(), ctl.Mirrored) }
	ctl.OnMove = a.rig.forgetTarget
	ctl.OnScan = a.rig.adoptFound
	if err := a.cap.preview(c.Plan); err != nil {
		log.Print(err)
	}
	// Unless the command line says what to connect: the board and the camera
	// used last, if they're here; else the only one there is (see
	// startupDevice). With none here, the page offers the simulators.
	go func() {
		p := *port
		if p == "" {
			var real []string
			ports, _ := listPorts()
			for _, pi := range ports {
				if pi.Likely { // a USB-serial adapter (not Bluetooth and the like)
					real = append(real, pi.Name)
				}
			}
			p = startupDevice(c.Port, real)
		}
		if p == "" {
			return
		}
		if err := a.rig.connect(ctx, p, c.Baud); err != nil {
			log.Printf("connect %s: %v", p, err)
		}
	}()
	go func() {
		cam := *camera
		if cam == "" {
			var real []string
			for _, ci := range listCameras() {
				if ci.ID != simCameraID {
					real = append(real, ci.ID)
				}
			}
			cam = startupDevice(c.Camera, real)
		}
		if cam == "" {
			return
		}
		if err := a.camera.connect(cam); err != nil {
			log.Printf("camera %s: %v", cam, err)
		}
	}()
	defer a.camera.disconnect()
	defer a.rig.disconnect()
	go a.rig.pollLoop(ctx)
	go ctl.Poll(ctx)
	go a.camera.pollBattery(ctx)

	mux := http.NewServeMux()
	site, _ := fs.Sub(webFiles, "web")
	mux.Handle("/", http.FileServerFS(site))
	mux.HandleFunc("/ws", a.handleWS)
	mux.HandleFunc("GET /photo/{file}", a.handlePhoto)
	mux.HandleFunc("POST /photo/sim", a.handleSimPhoto)
	mux.HandleFunc("GET /camera/preview", a.handlePreview)
	mux.Handle("/ctl/", http.StripPrefix("/ctl", ctl.Routes()))
	hs := &http.Server{Addr: *addr, Handler: mux}
	go func() {
		<-ctx.Done()
		a.cap.stop()
		hs.Close()
	}()
	open := *addr
	if strings.HasPrefix(open, ":") {
		open = "localhost" + open
	}
	log.Printf("listening on %s; open http://%s", *addr, open)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// ctlConfigPath is gosts-ctl's settings file (gosts-ctl/config.toml in the
// user's config directory), so Servo Ctl and gosts-ctl share it.
func ctlConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "gosts-ctl.toml"
	}
	return filepath.Join(dir, "gosts-ctl", "config.toml")
}

// startupDevice picks the device to connect on startup from the real ones
// found: the one used last if it's among them, else the only one ("" if
// none, or several and not the last one: the page then has them to pick from).
func startupDevice(last string, real []string) string {
	if last != "" && slices.Contains(real, last) {
		return last
	}
	if len(real) == 1 {
		return real[0]
	}
	return ""
}
