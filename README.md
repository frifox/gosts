# Go ST Servo

A Go package for Waveshare ST3215 serial bus servos: find them, move them, read how they're doing, and tune
them. Other Waveshare ST servos, and likely the original Feetech ST ones, should work too.

## What you need

- ST3215 servos.
- A **Waveshare Bus Servo Adapter (A)** on USB (or another serial adapter wired to the servo bus).
- A 6–12 V supply for the servos, into the adapter.

## Quick start

```bash
go get github.com/frifox/gosts
```

```go
bus, err := gosts.Open("/dev/ttyACM0", gosts.DefaultBaudRate) // macOS: /dev/cu.usbmodem…
if err != nil {
    log.Fatal(err)
}
defer bus.Close()

bus.SyncTorque(true, 1, 2)

// Move two servos together, then wait for one to get there.
bus.SyncMove(
    gosts.Target{ID: 1, Position: gosts.DegreesToSteps(90), Speed: 1500, Acc: 50},
    gosts.Target{ID: 2, Position: gosts.DegreesToSteps(200), Speed: 1500, Acc: 50},
)
bus.Servo(1).WaitForPosition(ctx, gosts.DegreesToSteps(90), gosts.WaitOptions{})

// Position, voltage, temperature, current and status of both, in one read.
fb, _ := bus.SyncFeedback(1, 2)
fmt.Printf("%.1f° %.1fV %d°C\n", fb[1].Degrees(), fb[1].Voltage, fb[1].Temperature)
```

## What you can do

- **Find servos** on the bus (`Scan`, `Ping`), and change their ID or baud rate.
- **Move** them: to a position, at a speed, as continuous rotation, or by raw PWM. Several servos start at the
  same instant with `SyncMove`.
- **Multi-turn**: let a servo turn more than once (about ±7.5 turns) and always take the short way round.
- **Limit** a servo to an arc it must never leave, e.g. so an arm can't hit the frame.
- **Watch** them: position, speed, load, voltage, temperature, current and faults, one servo or many per read.
- **Calibrate**: set where 0° is, or what the current position should read.
- **Tune** the position loop by hand, try settings until power-off without saving them, or auto-tune.
- **Raw access** to every register when you need it.

Settings that live in the servo's memory are saved properly (unlocked, written, locked again). The bus is safe to
use from several goroutines.

## Two servos on one axis

When two servos drive one joint from opposite sides, the same command would turn them against each other. Mark
one as **mirrored** and give both the same values:

```go
bus.SetMirrored(2, true)
```

A **group** then drives them as one, every member starting at the same instant:

```go
pitch := bus.Group(1, 2)  // leader first
pitch.Align(200, 10)      // bring the follower to the leader
pitch.MoveTo(1500, 1000, 30)
fb, _ := pitch.WaitForPosition(ctx, 1500, gosts.WaitOptions{})
if fb.Fighting(30) { /* they're pushing against each other */ }
```

Calibrate both so the same reading means the same pose. Mirroring isn't stored on the servo: set it each time
your program starts.

## Auto-tuning

The `autotune` package finds good position-loop settings for your servo, with its real load, by making small
test moves and keeping what moves most accurately and calmly. About 20–35 tests; the moves stay within ±35°,
and it stops and puts everything back if anything looks wrong.

```go
res, err := autotune.Run(ctx, autotune.ForGroup(bus.Group(1, 2)), autotune.Options{})
if err == nil {
    autotune.SaveParams(autotune.ForGroup(bus.Group(1, 2)), res.Best.Params)
}
```

Start it where the load pulls hardest: for an arm, level. Set up mirroring and calibration first.

## Example apps

<table>
<tr>
<td width="50%" valign="top">
<a href="cmd/gosts-ctl"><img src="docs/gosts-ctl.jpg" alt="gosts-ctl"></a><br>
<b><a href="cmd/gosts-ctl">gosts-ctl</a></b>: a web console to set up, tune and drive servos.
Find them, change IDs and zero, group them, auto-tune, watch them live.
</td>
<td width="50%" valign="top">
<a href="cmd/gosts-rig"><img src="docs/gosts-rig.jpg" alt="gosts-rig"></a><br>
<b><a href="cmd/gosts-rig">gosts-rig</a></b>: runs a turntable photogrammetry rig. Plans captures, moves the
rig, drives the camera and collects the photos.
</td>
</tr>
</table>

Both have a simulator, so you can try them without hardware:

```bash
go install github.com/frifox/gosts/cmd/gosts-ctl@latest && gosts-ctl --sim 1,2,3
go install github.com/frifox/gosts/cmd/gosts-rig@latest && gosts-rig --rig sim --camera sim
```

## Setup tips

- **Adapter**: set its jumper to the USB/PC position.
- **New servos all have ID 1.** Connect them one at a time and give each its own ID before chaining them.
- **Linux**: the adapter is `/dev/ttyACM0` or `/dev/ttyUSB0`; add yourself to the `dialout` group.
- **macOS**: use the `/dev/cu.*` device, not `/dev/tty.*`.
- **Higher supply voltage?** Servos ship with an 8 V limit: if `Feedback` reports a voltage fault, raise it with
  `SetVoltageLimits`.
- **Multi-turn**: a servo reports its position within one turn, but counts its goals across turns. Use
  `AbsolutePosition` (or `MoveToShortest`) rather than feeding the reported position back as a goal. The
  turn count resets at power-off.
- **PWM mode**: check the direction on your servos (the vendor's documents disagree on it).

## Tests

```bash
go test -race ./...
```

They run against a built-in servo emulator, no hardware needed.
