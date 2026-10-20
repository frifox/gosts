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
	"flag"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/frifox/gosts/cmd/gosts-ctl/console"
)

func main() {
	if !run() {
		return
	}
	// Settings' Restart: everything let go of (run's deferred: the camera,
	// the rig, this run's samples), gosts-rig starts again in place: the
	// same process (an app that started it, watching it, sees no exit),
	// the binary there now (one installed since: that one), its flags.
	exe, err := os.Executable()
	if err == nil {
		log.Print("restart: starting again")
		err = syscall.Exec(exe, os.Args, os.Environ())
	}
	log.Fatalf("restart: %v", err)
}

// run is gosts-rig, till it quits; whether it's to start again (Restart).
func run() bool {
	port := flag.String("rig", "", `the rig's board to connect on startup: its serial device, or "sim" for the simulated rig (default: Port in the config file)`)
	flag.StringVar(port, "port", "", "the same as --rig (its old name)")
	sim := flag.Bool("sim", false, "the same as --rig sim (its old name)")
	camera := flag.String("camera", "", `camera to connect on startup: its id (as the page lists it), or "sim" for the simulated camera (default: Camera in the config file)`)
	addr := flag.String("listen", "", "address to serve the page on, e.g. \":8081\", \"localhost:9000\" or just a port, 9000 (default: ListenAddr in the config file, \":8081\" if unset)")
	flag.StringVar(addr, "addr", "", "the same as --listen (its old name)")
	cfgPath := flag.String("config", defaultConfigPath(), "settings file, created if missing")
	photos := flag.String("photos", "", "folder the captures are saved in (default: PhotoDir in the config file, else ~/Pictures/gosts-rig)")
	ctlPath := flag.String("ctl-config", ctlConfigPath(), "the servo console's settings file (Servo Ctl: gosts-ctl's, shared with it), created on the first change")
	withStdin := flag.Bool("exit-with-stdin", false, "quit (cleanly) when stdin closes: for an app that starts gosts-rig, so it goes when the app does, even if the app crashes")
	flag.Parse()
	flag.Visit(func(f *flag.Flag) {
		flagsSet.photos = flagsSet.photos || f.Name == "photos"
		flagsSet.config = flagsSet.config || f.Name == "config"
	})

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
	switch {
	case *photos != "":
		photoDir = expandHome(*photos)
	case c.PhotoDir != "":
		photoDir = expandHome(c.PhotoDir)
	}
	log.Printf("photos: %s", photoDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *withStdin {
		go func() {
			io.Copy(io.Discard, os.Stdin)
			log.Print("stdin closed: quitting")
			stop()
		}()
	}

	a := &app{cfg: cfg, clients: map[*client]struct{}{}, quit: stop}
	a.rig = &rig{cfg: cfg, out: a.broadcast}
	a.camera = &cameraConn{out: a.broadcast, cfg: cfg}
	a.cap = &capture{rig: a.rig, camera: a.camera, out: a.broadcast}
	a.model = &modelBuilder{out: a.broadcast}
	a.camera.onPicture = a.cap.pictured // (the capture's shots: their files, saved as they come)
	a.camera.nameFor = a.cap.fileStem   // (named after their place in the plan: capt_001.jpg)
	startBatch(false)                   // a new capture each start: named now, its folder made as it's needed
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
	// The move speed and acceleration: the Rig card's, shared with Servo
	// Settings' Control card (one setting: a move from either goes the same
	// way); changed in either, both show it.
	ctl.Motion = func() (int, int, bool) { m := a.cfg.get().Motion; return m.Speed, m.Acc, true }
	ctl.SetMotion = func(speed, acc int) error {
		speed = max(50, min(3400, speed)) // (0 there: the servos' fastest; the rig's speeds are 5–298°/s)
		acc = max(1, min(254, acc))       // (0 there: no ramp at all)
		err := a.cfg.update(func(c *Config) { c.Motion.Speed, c.Motion.Acc = speed, acc })
		a.rig.sendState()
		return err
	}
	a.onMotion = ctl.BroadcastState
	// The simulator has Servo Settings of its own (beside the real rig's):
	// #1 and #2 a group, #1 mirrored, as the simulated rig has them.
	simCtlPath := filepath.Join(filepath.Dir(*ctlPath), "gosts-rig-sim.toml")
	a.rig.ctlFor = func(sim bool) {
		path := *ctlPath
		if sim {
			path = simCtlPath
		}
		if err := ctl.UseConfig(path); err != nil {
			log.Printf("servo console: %v", err)
			return
		}
		if sim {
			ro := simRoles(Roles{})
			if err := ctl.SeedGroup("Elevation", []uint8{ro.ElevationLeader, ro.ElevationFollower}, ro.ElevationLeader); err != nil {
				log.Printf("servo console: %v", err)
			}
		}
	}
	if err := a.cap.preview(c.Plan); err != nil {
		log.Print(err)
	}
	// Unless the command line says what to connect: the board and the camera
	// used last, if they're here; else the only one there is (see
	// startupDevice); else the first there is (the rig: a likely adapter);
	// with none (or it won't connect), the simulator, so it's ready to try.
	go func() {
		p, chosen := *port, *port != ""
		if !chosen {
			var real []string
			ports, _ := listPorts()
			for _, pi := range ports {
				if pi.Likely { // a USB-serial adapter (not Bluetooth and the like)
					real = append(real, pi.Name)
				}
			}
			p = startupDevice(c.Port, real)
			if p == "" && len(real) > 0 {
				p = real[0]
			}
			if p == "" {
				p = simPort
			}
		}
		if err := a.rig.connect(ctx, p, c.Baud); err != nil {
			log.Printf("connect %s: %v", p, err)
			if chosen || p == simPort {
				return
			}
			log.Printf("connect: the simulated rig instead")
			if err := a.rig.connect(ctx, simPort, c.Baud); err != nil {
				log.Printf("connect %s: %v", simPort, err)
			}
		}
	}()
	go func() {
		// The camera as the rig: the one used last, else the first there
		// is; with none (or it won't connect), the simulated camera.
		cam, chosen := *camera, *camera != ""
		if !chosen {
			var real []string
			for _, ci := range listCameras() {
				if ci.ID != simCameraID {
					real = append(real, ci.ID)
				}
			}
			cam = startupDevice(c.Camera, real)
			if cam == "" && len(real) > 0 {
				cam = real[0]
			}
			if cam == "" {
				cam = simCameraID
			}
		}
		if err := a.camera.connect(cam); err != nil {
			log.Printf("camera %s: %v", cam, err)
			if chosen || cam == simCameraID {
				return
			}
			log.Printf("camera: the simulated camera instead")
			if err := a.camera.connect(simCameraID); err != nil {
				log.Printf("camera %s: %v", simCameraID, err)
			}
		}
	}()
	cleanStaleSamples()   // (sample shots a run that's gone left)
	defer removeSamples() // (this run's: after the camera's gone)
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
	mux.HandleFunc("GET /batches", a.handleBatches)
	mux.HandleFunc("GET /batches/{name}", a.handleBatch)
	mux.HandleFunc("GET /batches/{name}/{file}", a.handleBatchPhoto)
	mux.HandleFunc("POST /batches/{name}/rename", a.handleBatchRename)
	mux.HandleFunc("POST /batches/{name}/move", a.handleBatchMove) // (a folder into another: Browse's Move to…)
	mux.HandleFunc("POST /batches/new", a.handleBatchNew)          // (Browse's New Folder)
	mux.HandleFunc("GET /batches/{name}/model", a.handleModelInfo)
	mux.HandleFunc("GET /batches/{name}/models", a.handleModels)
	mux.HandleFunc("GET /batches/{name}/models/{model}/{file}", a.handleModelFile)
	mux.HandleFunc("GET /batches/{name}/models/{model}/thumb", a.handleModelThumb)
	mux.HandleFunc("PUT /batches/{name}/models/{model}/thumb", a.handleModelThumbPut)
	mux.HandleFunc("POST /batches/{name}/models/{model}/open", a.handleModelOpen)
	mux.HandleFunc("POST /batches/{name}/models/{model}/delete", a.handleModelDelete)
	mux.HandleFunc("POST /batches/{name}/models/{model}/rename", a.handleModelRename)
	mux.HandleFunc("POST /batches/{name}/models/{model}/move", a.handleModelMove) // (Browse's Move to…)
	mux.HandleFunc("POST /batches/{name}/models/{model}/save", a.handleModelSave)
	mux.HandleFunc("POST /combine/save", a.handleCombineSave) // (Combine Models: what it made)
	mux.HandleFunc("GET /combine/works", a.handleWorks)       // (Combine Models' work files: each by its path, ?path=)
	mux.HandleFunc("GET /combine/work", a.handleWork)
	mux.HandleFunc("GET /combine/work/thumb", a.handleWorkThumb)
	mux.HandleFunc("POST /combine/work", a.handleWorkSave)
	mux.HandleFunc("POST /combine/work/delete", a.handleWorkDelete)
	mux.HandleFunc("POST /combine/work/rename", a.handleWorkRename)
	mux.HandleFunc("POST /combine/work/move", a.handleWorkMove)
	mux.HandleFunc("POST /batches/{name}/delete", a.handleBatchDelete)
	mux.HandleFunc("POST /batches/{name}/{file}/delete", a.handleBatchPhotoDelete)
	mux.Handle("/ctl/", http.StripPrefix("/ctl", ctl.Routes()))
	hs := &http.Server{Handler: mux}
	if err := a.listen(hs, *addr); err != nil { // (where it can change: Settings' Public)
		log.Fatal(err)
	}
	open := *addr
	if strings.HasPrefix(open, ":") {
		open = "localhost" + open
	}
	log.Printf("listening on %s; open http://%s", *addr, open)
	<-ctx.Done()
	a.cap.stop()
	hs.Close()
	return a.restart.Load()
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

// expandHome is p with a leading ~ for the home folder.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// homeShort is p with the home folder as ~ (for the page).
func homeShort(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if p == home {
			return "~"
		}
		if strings.HasPrefix(p, home+string(filepath.Separator)) {
			return "~" + strings.TrimPrefix(p, home)
		}
	}
	return p
}
