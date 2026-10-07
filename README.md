# Go ST Servo

Go package built to control Waveshare ST3215 servo, but should work with other Waveshare ST and possibly the original Feetech ST servos too.
Servos are driven by the **Waveshare Bus Servo Adapter (A)** but connecting to servo directly should also work.

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

## Example apps

### [gosts-ctl](cmd/gosts-ctl): web console

<p align="center"><a href="cmd/gosts-ctl"><img src="docs/gosts-ctl.jpg" width="480" alt="gosts-ctl"></a></p>

A WebSocket server plus a web page to set up, monitor and drive the servos: pick the driver board, find
the servos, then configure, tune and drive them (IDs, zero, limits, mirrored groups, auto-tuning, live
telemetry).

```bash
go install github.com/frifox/gosts/cmd/gosts-ctl@latest
gosts-ctl -sim 1,2,3    # simulated servos, no hardware needed
```

More in [its README](cmd/gosts-ctl/README.md).

### [gosts-rig](cmd/gosts-rig): photogrammetry rig

<p align="center"><a href="cmd/gosts-rig"><img src="docs/gosts-rig.jpg" width="480" alt="gosts-rig"></a></p>

Drives a photogrammetry rig built from 2020 extrusions: a 600×500 mm base with a
600 mm post in the middle of each long side, an ST3215 on top of each post tilting a 600×450 mm frame
(camera on one 450 mm bar, counterweight on the other) for the camera's **elevation**, and a third
ST3215 turning the platform under the object for the **azimuth**. It plans and runs captures (stopping for
each shot, or moving shots along a spiral), and drives the camera over gphoto2: settings, sample shots, gray
card white balance, focus and depth of field.

```bash
go install github.com/frifox/gosts/cmd/gosts-rig@latest
gosts-rig --rig sim --camera sim    # simulated rig and camera, no hardware needed
```

More in [its README](cmd/gosts-rig/README.md).

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
