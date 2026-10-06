// Command gosts drives a photogrammetry rig built on ST3215 servos: two
// servos on top of the posts tilt the camera frame (elevation) as one
// mirrored group, a third turns the platform under the object (azimuth).
// The web page plans a capture (rings of photos round the object) and shows
// the rig live.
//
// Set up and calibrate the servos with gosts-ctl first (IDs, zero, tuning,
// limits); gosts and gosts-ctl can't use the serial port at the same time.
//
//	go install github.com/frifox/gosts/cmd/gosts@latest
//
//	gosts                               # pick the board in the browser
//	gosts -port /dev/cu.usbmodem1101    # connect on startup
//	gosts -sim                          # simulated rig (servos 10, 11, 12)
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
	"strings"
)

func main() {
	port := flag.String("port", "", "serial device to connect to on startup (default: Port in the config file)")
	sim := flag.Bool("sim", false, "use a simulated rig")
	addr := flag.String("addr", "", "web address, e.g. localhost:8081 (default: ListenAddr in the config file, \":8081\" if unset)")
	cfgPath := flag.String("config", defaultConfigPath(), "settings file, created if missing")
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
	if *port == "" && !*sim {
		*port = c.Port
	}
	if *sim {
		*port = simPort
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	a := &app{cfg: cfg, clients: map[*client]struct{}{}}
	a.rig = &rig{cfg: cfg, out: a.broadcast}
	a.cap = &capture{rig: a.rig, out: a.broadcast}
	if err := a.cap.preview(c.Plan); err != nil {
		log.Print(err)
	}
	if *port != "" {
		go func() {
			if err := a.rig.connect(ctx, *port, c.Baud); err != nil {
				log.Printf("connect %s: %v", *port, err)
			}
		}()
	}
	defer a.rig.disconnect()
	go a.rig.pollLoop(ctx)

	mux := http.NewServeMux()
	site, _ := fs.Sub(webFiles, "web")
	mux.Handle("/", http.FileServerFS(site))
	mux.HandleFunc("/ws", a.handleWS)
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
