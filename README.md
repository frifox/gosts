# Go ST Servo

Go package built to control Waveshare ST3215 servo, but should work with other Waveshare ST and possibly the original Feetech ST servos too.
Servos are driven by the **Waveshare Bus Servo Adapter (A)** but connecting to servo directly should also work.

![gosts-ctl web console controlling ST3215 servos](docs/screenshot.jpg)

*[gosts-ctl](#gosts-ctl-web-console), the web console, driving ST3215 servos.*

```go
import "github.com/frifox/gosts"

bus, err := gosts.Open("/dev/ttyACM0", gosts.DefaultBaudRate) // 1 Mbps, factory default
if err != nil {
    log.Fatal(err)
}
defer bus.Close()

pan, tilt := bus.Servo(1), bus.Servo(2)
bus.SyncTorque(true, 1, 2)

// Move both servos at the same time, then wait until each one arrives.
bus.SyncMove(
    gosts.Target{ID: 1, Position: gosts.DegreesToSteps(90), Speed: 1500, Acc: 50},
    gosts.Target{ID: 2, Position: gosts.DegreesToSteps(200), Speed: 1500, Acc: 50},
)
opt := gosts.WaitOptions{FailOn: gosts.StatusOverload}
pan.WaitForPosition(ctx, gosts.DegreesToSteps(90), opt)
tilt.WaitForPosition(ctx, gosts.DegreesToSteps(200), opt)

// Read the status of both servos in one round trip.
fb, _ := bus.SyncFeedback(1, 2)
fmt.Printf("%.1f° %.1fV %d°C %.0fmA %s\n",
    fb[1].Degrees(), fb[1].Voltage, fb[1].Temperature, fb[1].Current, fb[1].Status)
```

## What it covers

| Area | API |
|---|---|
| Discovery | `Bus.Scan`, `Bus.Ping`, `Bus.Identify` (one servo on the bus) |
| Position mode | `MoveTo`, `MoveToDegrees`, `MoveToAndWait`, `Stop`, `SetAcceleration` |
| Multi-turn | `SetMultiTurn(true)` → goal range ±30719 steps (±7.5 turns) |
| Shortest path | `MoveToShortest` / `ShortestGoal` (servo and group: each member's goal in its own turn count), `NearestEquivalent`: in multi-turn mode, go the short way round across 0°/360° |
| Turn count | `AbsolutePosition` (position including the turn, in goal coordinates), `CircularDiff` (compare positions across the seam) |
| Other modes | `SetMode(ModeWheel/ModePWM/ModeStep)`, `SetWheelSpeed`, `SetPWM` |
| Multiple servos | `Bus.SyncMove`, `Bus.SyncTorque`, `Bus.SyncFeedback`, `RegMoveTo` + `Bus.Action` |
| Motion range | `Bus.SetRange(id, Range{Lo, Hi})`: keep a servo on one arc (single- and multi-turn); goals are clamped to it and reached along it, never across the gap. `MotionLimits` for the allowed goals around the present position |
| Mirroring | `Bus.SetMirrored(id, true)` / `WithMirrored(ids...)` for a servo mounted facing its partner: the same commands move both in sync |
| Groups | `bus.Group(ids...)`: `MoveTo`, `EnableTorque`, `SetMultiTurn`, `SetPositionAs`, `SetWheelSpeed`, `Align`, `CopyFromLeader`, `WaitForPosition`, `Feedback().Spread()` / `.Fighting()` (one packet per command) |
| Auto-tuning | `autotune.Run(ctx, autotune.ForServo(s) / ForGroup(g), opts)`: finds P, D, start force and dead zone by test moves with the real load |
| Monitoring | `Feedback` (position, speed, load, voltage, temperature, current, moving, status in one read), plus single getters |
| Torque | `EnableTorque`, `Hold` (torque on where it is: restarts the multi-turn count from the reading, so a stale goal, e.g. after rezeroing with torque off, can't send it a turn; also on groups), `SetTorqueLimit` (runtime), `SetMaxTorque` (persisted) |
| Calibration | `SetZero(steps)` / `Zero()` (absolute: the servo's own 0° on the encoder scale, e.g. 3072 = 270°), `SetZeroAt(steps)` (relative: the mark that reads `steps` now becomes 0°), `SetPositionAs(pos)` (current position reads `pos`), `ResetZero()` (offset 0, raw encoder angle; a factory reset instead restores the servo's factory calibration offset), `CalibrateMiddle` (= 2048), `SetPositionOffset`. A servo holding torque doesn't move; with torque off the goal is left alone |
| Configuration | `SetID`, `SetBaudRate`, `SetAngleLimits`, `SetVoltageLimits`, `SetMaxTemperature`, `SetProtection`, `SetPID`, `SetDeadZone`, `ReadConfig` |
| Raw access | `Servo.Read(reg)` / `Servo.Write(reg, v)` for every register in `Registers`; `Bus.Read/Write/SyncRead/SyncWrite` |
| Tuning trials | `Servo.WriteTemporary(reg, v)` applies EEPROM settings (PID, dead zone, protection...) until power-off without saving them |
| Faults | `Status` bit set (voltage, sensor, temperature, current, angle, overload); `WithStatusHandler` callback |

EEPROM writes are wrapped in unlock → write → lock automatically so they survive power cycles.
The `Bus` is safe for concurrent use.

## Mirrored servos

When two servos drive one axis from opposite sides (facing each other), the same command turns
them in opposite directions. Mark one as mirrored and use the same logical values for both:

```go
bus.SetMirrored(2, true) // or gosts.Open(dev, baud, gosts.WithMirrored(2))

bus.SyncMove(
    gosts.Target{ID: 1, Position: 1500, Speed: 1000, Acc: 30},
    gosts.Target{ID: 2, Position: 1500, Speed: 1000, Acc: 30}, // physically 4096-1500
)
```

For a mirrored servo every typed call works in logical coordinates: positions are reflected
about 2048 (`4096 - p`), and speed, load, current, wheel speed, PWM duty and `StepBy` steps
change sign. Angle limits are reflected, and `ReadConfig`, `Feedback`, `SyncFeedback` and
`WaitForPosition` report logical values. Raw register access (`Servo.Read/Write`,
`Bus.Read/Write/SyncRead/SyncWrite`) and `PositionOffset` stay physical.

Calibrate both servos so 2048 is the same mechanical pose (`CalibrateMiddle` in that pose, or
`SetPositionOffset`), otherwise they track with a constant offset. The flag is kept on the
`Bus` by ID (it follows `SetID`) and is not stored on the servo, so set it each time your
program starts.

## Servo groups

`Group` drives several servos as one: each command is a single SYNC WRITE, so all members start
at the same instant, and positions are logical, so mirrored members follow too.

```go
pitch := bus.Group(1, 2)               // leader first
pitch.Align(200, 10)                   // bring members to the leader's position
pitch.EnableTorque(true)
pitch.MoveTo(1500, 1000, 30)
fb, _ := pitch.WaitForPosition(ctx, 1500, gosts.WaitOptions{})
if fb.Spread() > 20 || fb.Fighting(30) { /* members disagree on a shared axis */ }
```

## Auto-tuning

The `autotune` sub-package finds position-loop settings by experiment: it applies candidate values
(until power-off), makes test moves around the current position with the real load, and scores each
response for accurate and calm motion (stopping short of the goal, overshoot, wobble, hunting, and for
groups how much the members disagree). It hill-climbs P, D, I, start force, dead zone and the move
acceleration in about 20–35 tests.

Start it where the load pulls hardest: for an unbalanced arm, horizontal (gravity's pull is largest
there, smallest pointing straight up or down). The tests move up against gravity and down with it, in
large moves and small steps, each back to the start, so the sag is measured where it is worst; I is
what removes it, and it is only kept when it doesn't add wobble. The acceleration (`Params.Acc`) isn't a
servo setting: pass it with your moves (gosts-ctl keeps it in `config.toml`).

Moves stay within ±35° and anything beyond 45° aborts; faults, high current or temperature abort too
(temperature and current only when they stay over the limit for 300 ms / 100 ms, so a single bad
reading is ignored), and an abort restores the original values.

```go
res, err := autotune.Run(ctx, autotune.ForGroup(bus.Group(1, 2)), autotune.Options{})
if err == nil {
    fmt.Println(res.Before.Metrics, "→", res.Best.Metrics) // Best is active until power-off
    autotune.SaveParams(autotune.ForGroup(bus.Group(1, 2)), res.Best.Params)
}
```

Tune with the real load mounted, near the middle of the range you'll use, after mirroring and center
calibration are set up. Grouped servos are always tuned together.

## gosts-ctl: web console

[`cmd/gosts-ctl`](cmd/gosts-ctl) is a WebSocket server plus a web page to set up, monitor and drive the servos.
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

Code layout: [`web`](cmd/gosts-ctl/web) (HTTP + WebSocket, the page in `web/dist`),
[`board`](cmd/gosts-ctl/board) (serial ports, connection, scanning), [`servo`](cmd/gosts-ctl/servo)
(commands on the servo motors and groups), [`servo-sim`](cmd/gosts-ctl/servo-sim) (simulated board),
[`internal`](cmd/gosts-ctl/internal) (settings file, messages, shared helpers); `cmd/gosts-ctl` itself ties
them together (`app*.go`).

gosts-ctl adds `github.com/gorilla/websocket` and `github.com/BurntSushi/toml` to the module; the
library packages don't import them. Listing USB port details uses cgo on macOS (the Xcode command
line tools).

## gosts: photogrammetry rig

[`cmd/gosts`](cmd/gosts) drives a photogrammetry rig built from 2020 extrusions: a 600×500 mm base with a
600 mm post in the middle of each long side, an ST3215 on top of each post tilting a 600×450 mm frame
(camera on one 450 mm bar, counterweight on the other) for the camera's **elevation**, and a third
ST3215 turning the platform under the object for the **azimuth**.

```bash
go install github.com/frifox/gosts/cmd/gosts@latest

gosts                               # pick the board in the browser (http://localhost:8081)
gosts -sim                          # simulated rig, no hardware needed
gosts -sim -camera sim              # and a simulated camera
```

Set up and calibrate the servos with gosts-ctl first (IDs, tuning, and "Set 0° to" so elevation 0° is
the camera level with the object and azimuth 0° is the platform's front); the two programs can't use
the serial port at the same time. In gosts, **Setup** assigns the roles (by default #10 elevation
leader, mirrored; #11 elevation follower; #12 azimuth) and the speed. The rig's servos are always switched
to multi-turn when gosts connects, so moves never take the long way round. Elevation moves go straight
from where each elevation servo is to the target (kept within the min/max), each worked out in that servo's
own turn count. Torque on holds the servos where they are: each restarts its turn count from its reading
(multi-turn off and on) and holds that, first at 35% torque; if one moves away or strains instead, torque
goes off again with an error naming it.

The page has the controls on the left (a **Motion** card that, until a board is connected, lists the serial
ports to connect to, with Refresh; once connected, live elevation/azimuth: the rig moves as a slider is dragged or a value entered, with Stop shown while it moves, and Torque, and a
**Capture** card that, until a camera is connected, lists the cameras to connect to (for now the
simulated one, whose photos are the 3D view rendered from the camera on the rig, looking at the
object, and any camera [gphoto2](http://gphoto.org) finds on USB, e.g. a Sony A6600 in PC Remote mode),
and once connected has the number of photos, the settle time, and **Moving Shots**, with Start,
Pause and Stop; Setup can disconnect either) and on the right a **Camera** card (a timeline of every photo
taken, newest on the left, during a capture too, each first as a placeholder while it's taken and comes
over; hovering one shows a delete button, which takes it off the timeline (its files stay); click one to see it full size, ← → to step, Esc to close;
**Reset**, which clears the timeline, **View All**, every photo in a grid over the whole page where a click
shows one full size (← → to step, Esc, ← All Photos or a click beside the photo back to the grid where it was left, ✕ to close), and **Take Photo**) above the **Rig View**, a live perspective view
of the rig on the right (a 3D model rendered with [three.js](https://threejs.org): 2020 extrusions, the
ST3215s, the Sony A6600 with its ring flash, and [#3DBenchy](https://www.3dbenchy.com) (CC0) as the
object; bundled in the binary, so no internet is needed), with
every planned shot turning with the platform and turning green once taken; **Preview** animates the
capture as the rig does it: the camera tilts while the turntable turns the object, the sphere of shots
and a fading trail with it, coloured by the camera's speed against the path's average (neutral at normal, amber to coral up to 2×, blue down to ½×),
with a speed key showing the speed now in °/s. Each photo pops its dot and fires the ring flash lightly. A running capture looks the same but follows the real rig (telemetry, smoothed), each
shot's dot going as its photo is taken; when it ends the trail runs out and the dots return, taken ones green. The photos are spread as evenly as possible over the part of the sphere round the object the
camera can reach (the min/max elevation in Rig Setup, which keeps the frame clear of the posts), on a
golden-angle spiral, and taken in rows of similar elevation, each row in azimuth order with
alternating direction, so the arm moves little and the platform never unwinds a whole turn. With
**Moving Shots** (for a fast shutter) the rig doesn't stop: the same shots are taken along one smooth
rising spiral, the platform turning one way while the camera climbs: the rig follows the same smooth curve
the preview draws (goals streamed ~30 times a second just ahead of it, corrected for how far it trails, within
the servos' speed and with gentle acceleration), each photo taken as the path passes its shot; the platform is never unwound: if a spiral
wouldn't stay within its servo's multi-turn range (about ±7.5 turns from power-up) from where it is, it turns
the other way instead (the same shots, mirrored), so spirals are limited to 7 turns (about 90 photos).
When the last photo is taken (or a preview reaches its last shot, if a board is connected) the rig returns to 0°/0° the short way (less than a turn) and stops. Each photo is
taken with the connected camera, recording where the rig really is as the shutter goes.

A real camera is driven through gphoto2 (`brew install gphoto2`). Set it to RAW & JPEG (and, for a Sony in
PC Remote, save to PC+Camera): each photo's RAW stays on the camera's card and only the JPEG comes over USB
(much quicker), saved in `~/Pictures/gosts/<date>/` (`PhotoDir` in the config file changes it) and shown on
the page. A camera on JPEG only works the same way; on RAW only no picture would come. On macOS the
system's camera service grabs a camera when it's plugged in (and may open Photos): if taking a photo says
another program has the camera, quit Photos and Image Capture and run `killall ptpcamerad mscamerad-xpc`.
A photo still takes a few seconds (about 3 s with the A6600's large JPEG), which Moving Shots can't wait for yet.

The **Rig Setup** card (below Motion; like every card it folds by its title, each window remembering which are folded) sets the rig's measurements for the 3D view (in mm, seen from above: base X/Y, post Z, swing
X/Y, the camera's offset along the arms (camera X) and up/down from the bar (camera Z, default 0), the turntable's height (turntable Z), the object's height (object height, the model is scaled to it); the defaults draw the rig to scale)
and the min/max elevation the camera may reach (default −45° to 80°).
The view has 3D, Front (from behind the camera, looking at the object), Side (along the tilt axis) and Top
presets.

Settings are kept in `gosts/config.toml` in the user's config directory (`-config` to change).

## Platform notes

- **Adapter**: set the jumper on the Bus Servo Adapter (A) to the USB/PC position.
  Power the servos from the adapter's DC input (6–12 V per the Waveshare manual). The
  memory table's factory `MaxVoltage` is 8.0 V. If your supply is higher, check the `Status` in `Feedback`
  for a `voltage` status and raise it with `SetVoltageLimits`.
- **Linux**: the adapter shows up as `/dev/ttyACM0` or `/dev/ttyUSB0`. Add your user to
  the `dialout` group (`uucp` on Arch). For lower latency with FTDI/CH34x drivers,
  `setserial /dev/ttyUSB0 low_latency` helps when polling many servos quickly.
- **macOS**: use the `/dev/cu.*` device (not `/dev/tty.*`), e.g. `/dev/cu.usbmodem…` or
  `/dev/cu.wchusbserial…`. Recent macOS includes the CH34x driver.
- Each new servo ships with ID 1. Connect them **one at a time** and give each a unique
  ID (`Servo.SetID`, or the Setup panel of gosts-ctl) before chaining them.

## Multi-turn mode: positions wrap

Verified on hardware: in multi-turn mode the ST3215 still **reports** its position within one turn
(0..4095), but **goals** use its own unbounded turn count. Using the reported position as a goal can
therefore send the servo a whole turn the wrong way. Use `AbsolutePosition()` for the present
position in goal coordinates (the turn is taken from the goal register); `Stop`, `MoveToShortest`,
`Group.Align` and the auto-tuner already do. The turn count is not kept across power cycles.

## Things to verify on hardware

- `PWMSignBit`: the vendor memory table says the PWM direction bit is bit 11, while the
  vendor SC-series library uses bit 10. The package uses bit 10 — check the direction
  in `ModePWM` if you use it.
- `SetBaudRate`: after changing it, reopen the bus at the new speed.

## Tests

```bash
go test -race ./...
```

The tests run against an in-memory servo emulator and check packet encoding against the
example frames in the manufacturer's protocol manual.
