# gosts-ctl: web console

An example app of [Go ST Servo](../../README.md) (`github.com/frifox/gosts`).

<p align="center"><img src="../../docs/gosts-ctl.jpg" alt="gosts-ctl web console controlling ST3215 servos"></p>

`gosts-ctl` is a WebSocket server plus a web page to set up, monitor and drive the servos.
The page walks through three steps:

1. **Driver board**: lists serial ports with their USB details and marks likely adapters
   (WCH CH34x, FTDI, CP210x, PL2303). Pick one (or the built-in simulator) and a baud rate.
2. **Find servos**: scans an ID range (quick 0–20 or full 0–253) with live progress.
3. **Monitor & control**: only the servos found are shown. You get a live position dial, telemetry,
   30 s history charts, controls for every mode, setup actions (0° calibration, motion range limits, ID change,
   multi-turn), a Tuning card (position loop gains, dead zones, start force, torque and protection,
   with presets and **Auto…** tuning; **Try** applies until power-off, **Save** persists) and an
   editable view of the full memory table.

```bash
go install github.com/frifox/gosts/cmd/gosts-ctl@latest

gosts-ctl                               # pick the board in the browser
gosts-ctl -port /dev/ttyACM0            # connect + scan on startup (Linux)
gosts-ctl -port /dev/cu.usbmodem1101    # connect + scan on startup (macOS)
gosts-ctl -sim 1,2,3                    # simulated servos, no hardware needed
```

From a checkout, `go run ./cmd/gosts-ctl` does the same.

Then open http://localhost:8080. The console listens on `ListenAddr` from `config.toml` (default
`:8080`, every network interface; set `"localhost:8080"` to keep it to this machine), or on `-addr`
when given. Other flags: `-baud`, `-poll` (telemetry interval, default 50 ms), `-nosync` (poll servos
one by one if SYNC READ misbehaves) and `-config`.

Per-servo settings that can't be stored on the servo are kept in `config.toml`, keyed by servo ID:
`~/.config/gosts-ctl/config.toml` on Linux, `~/Library/Application Support/gosts-ctl/config.toml` on
macOS (or `-config path`). A motion range set in Setup is kept there
too (`Range = [lo, hi]`, encoder-scale steps) and enforced in multi-turn mode as well. The web UI writes it when you
edit a servo (pencil next to its name: name, color, mirrored, ±180° angles, virtual 0°, dial orientation) or change an ID. Edits made by hand are read on startup:

```toml
ListenAddr = ":8080" # web console address

[1]
Name = "Left"

[2]
Name = "Right"
Mirrored = true
Signed = true  # show angles as -180..180 instead of 0..360
WeightComp = true # nudge the goal until a sagging arm reaches it (Weight Comp toggle)
Zero = 270.0   # virtual 0°: physical 270° is shown as 0°, straight up as 90°
DialUp = 340.0 # dial orientation: the arm points physically up at encoder 340°
```

**Groups** (sidebar → New group) drive their members together from one console: one needle and
one chart line per servo (each servo keeps its own color everywhere), a row per member, and a Group
health card with spread, opposing load, fight protection (warn, or cut torque), Align and Copy
tuning (plus Auto-tune group). Groups and colors are stored in `config.toml`:

```toml
[group.pitch]
Name = "Pitch"
Members = [1, 2]        # leader first
OnFight = "torque-off"  # default "warn"
```

Several browser windows can be open at once: connection, scan, telemetry, names, mirrored state and
servo settings stay in sync across them (each window still picks its own selected servo).

Code layout: [`web`](web) (HTTP + WebSocket, the page in `web/dist`),
[`board`](board) (serial ports, connection, scanning), [`servo`](servo)
(commands on the servo motors and groups), [`servo-sim`](servo-sim) (simulated board),
[`internal`](internal) (settings file, messages, shared helpers); `cmd/gosts-ctl` itself ties
them together (`app*.go`).

gosts-ctl adds `github.com/gorilla/websocket` and `github.com/BurntSushi/toml` to the module; the
library packages don't import them. Listing USB port details uses cgo on macOS (the Xcode command
line tools).
