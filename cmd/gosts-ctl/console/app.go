package console

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/frifox/gosts"
	"github.com/frifox/gosts/cmd/gosts-ctl/board"
	"github.com/frifox/gosts/cmd/gosts-ctl/internal"
	"github.com/frifox/gosts/cmd/gosts-ctl/servo"
	"github.com/frifox/gosts/cmd/gosts-ctl/web"
)

// App ties gosts-ctl together: it builds the web server, the driver board and
// the servo controller, runs the browser's requests on them (as the web
// package's Handler), polls telemetry, keeps every window in sync, and is the
// Notifier the other packages report through.
type App struct {
	cfg    *internal.Config
	srv    *web.Server
	board  *board.Board
	ctl    *servo.Controller
	poll   time.Duration
	noSync bool

	// OnChange, if set, is called after the groups or a servo's mirroring
	// changed (gosts-rig assigns the rig's roles from them).
	OnChange func()
	// OnMove, if set, is called after a request that moves servos, or lets
	// them be moved, or changes where their 0° is (gosts-rig's own goals for
	// them no longer hold).
	OnMove func()
	// OnScan, if set, is called with the servos a scan found (gosts-rig
	// takes them: its board's servos are the same).
	OnScan func(ids []uint8)

	mu      sync.Mutex
	refresh map[uint8]*pendingRefresh
	at      *autotuneRun // current or last auto-tune run
}

// New builds the app. simIDs are the servos of the simulated board; poll is
// the telemetry interval; noSync polls servos one by one instead of SYNC READ.
func New(cfg *internal.Config, simIDs []uint8, poll time.Duration, noSync bool) *App {
	a := &App{cfg: cfg, poll: poll, noSync: noSync}
	a.srv = web.New(a)
	a.board = board.New(cfg, a, simIDs)
	a.board.OnScan = func(ids []uint8) {
		if a.OnScan != nil {
			a.OnScan(ids)
		}
	}
	a.ctl = servo.New(cfg, a)
	return a
}

// Logf logs a message and shows it in every window's log.
func (a *App) Logf(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Print(msg)
	a.srv.Broadcast(internal.LogMsg{Type: "log", Level: level, Message: msg})
}

// Broadcast sends a message to every window.
func (a *App) Broadcast(msg any) { a.srv.Broadcast(msg) }

// BroadcastState sends the current state to every window.
func (a *App) BroadcastState() { a.srv.Broadcast(a.stateMsg()) }

// Refresh re-reads servo id's settings soon and sends them to every window.
func (a *App) Refresh(id uint8) { a.scheduleRefresh(id, -1) }

func (a *App) stateMsg() internal.StateMsg {
	st := a.board.Status()
	mirrored, signed, names, colors, dialUps := []int{}, []int{}, map[string]string{}, map[string]string{}, map[string]float64{}
	ranges, weightComp, accs := map[string][]int{}, []int{}, map[string]int{}
	for id, sc := range a.cfg.All() {
		key := strconv.Itoa(int(id))
		if sc.Mirrored {
			mirrored = append(mirrored, int(id))
		}
		if sc.Signed {
			signed = append(signed, int(id))
		}
		if sc.WeightComp {
			weightComp = append(weightComp, int(id))
		}
		if sc.Acc != 0 {
			accs[key] = sc.Acc
		}
		if len(sc.Range) == 2 {
			ranges[key] = sc.Range
		}
		if sc.Name != "" {
			names[key] = sc.Name
		}
		if sc.Color != "" {
			colors[key] = sc.Color
		}
		if sc.DialUp != 0 {
			dialUps[key] = sc.DialUp
		}
	}
	slices.Sort(mirrored)
	slices.Sort(signed)
	slices.Sort(weightComp)
	return internal.StateMsg{Type: "state", Connected: st.Port != "", Port: st.Port, Baud: st.Baud,
		Scanning: st.Scanning, Scanned: st.Scanned, IDs: internal.ToInts(st.IDs), Mirrored: mirrored, Signed: signed, WeightComp: weightComp, Accs: accs, Names: names,
		Colors: colors, DialUps: dialUps, Ranges: ranges, Groups: groupInfos(a.cfg.AllGroups())}
}

// Run serves the console on addr until ctx is done. With port set it first
// connects to that driver board (board.SimPort for the simulator) and scans
// it for servos.
func (a *App) Run(ctx context.Context, addr, port string, baud int) error {
	if port != "" {
		if err := a.board.Connect(port, baud); err != nil {
			return err
		}
		go a.board.Scan(ctx, 0, gosts.MaxID)
	}
	defer func() {
		a.stopAutotune()
		a.board.Disconnect()
	}()
	go a.pollLoop(ctx)
	return a.srv.ListenAndServe(ctx, addr)
}

// Embedding: another program that owns the driver board (gosts-rig) serves
// the console within its own page. It mounts Routes (the page at /, the
// WebSocket at ws), runs Poll, and attaches the console to its bus while it's
// connected.

// Routes is the console's page (/) and WebSocket (ws), relative: mount it
// under a prefix with http.StripPrefix. The page opened with ?embed hides its
// own header and the choosing of the board.
func (a *App) Routes() http.Handler { return a.srv.Routes() }

// Poll streams telemetry to the console's windows (while there are any) until
// ctx is done.
func (a *App) Poll(ctx context.Context) { a.pollLoop(ctx) }

// Attach works the console on bus, a board another program connected to port
// at baud, with the servos it found (ids): the console doesn't close it, nor
// connect to another. bus is best a view of that program's (gosts.Bus.View):
// the console's mirroring and motion ranges are its own.
func (a *App) Attach(bus *gosts.Bus, port string, baud int, ids []uint8) {
	a.stopAutotune()
	a.board.Attach(bus, port, baud, ids)
}

// Detach lets go of an attached bus (the other program disconnecting).
func (a *App) Detach() {
	a.stopAutotune()
	a.board.Disconnect()
}

// Groups are the groups' members, each leader first.
func (a *App) Groups() [][]uint8 {
	var out [][]uint8
	for _, g := range a.cfg.AllGroups() {
		out = append(out, g.Members)
	}
	return out
}

// Mirrored reports whether servo id is mirrored in the console's settings.
func (a *App) Mirrored(id uint8) bool { return a.cfg.Get(id).Mirrored }

// LoadConfig reads the console's settings file (servo names, mirroring, zero,
// ranges, groups), created on the first change; warnings are about entries
// it ignored.
func LoadConfig(path string) (*internal.Config, []string, error) { return internal.LoadConfig(path) }
