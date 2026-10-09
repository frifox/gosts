# Go ST Servo: Photogrammetry Rig

Runs a turntable photogrammetry rig from the browser: plan a capture, watch the rig move in 3D, and get the photos
straight off the camera. An example app of [Go ST Servo](../../README.md).

<p align="center"><img src="../../docs/gosts-rig.jpg" alt="gosts-rig: a moving-shots capture under way, the photos coming in and the rig drawn live"></p>

## The rig

- Based on aluminum extrusion profiles
- Two ST3215 servos on the posts tilt a frame carrying the camera
- One ST3215 turns the platform under the target object
- A camera on USB that [gphoto2](http://gphoto.org) can drive

No hardware? Simulators are available for both the rig and the camera.

## Install and run

```bash
brew install gphoto2                        # for a real camera
go install github.com/frifox/gosts/cmd/gosts-rig@latest

gosts-rig                                   # then open http://localhost:8081
gosts-rig --rig sim --camera sim            # simulated rig and camera
gosts-rig --listen :9000                    # another port (or "localhost:9000" to keep it to this computer)
```

## First time

1. Set up the servos with [gosts-ctl](../gosts-ctl) first: IDs, tuning, and zero (elevation 0° = camera level with
   the object, azimuth 0° = the platform's front). Close it after: only one program can use the serial port.
2. In gosts-rig, **Setup** picks which servo does what (by default #1 and #2 elevation, #3 azimuth).
3. The **gear** in the Rig card's title opens **Rig Setup**: your rig's measurements and how low and high the camera may go.
4. Set the camera to **RAW & JPEG** (on a Sony in PC Remote: save to PC+Camera). The RAWs stay on the card,
   and only the JPEGs come over USB.

## Taking photos

- In the **Camera** card, choose how many photos you want. They're spread evenly round the object, as high and low as the
  camera may go.
- **Moving Shots**: the rig doesn't stop for each photo. It glides along one smooth spiral instead, which is much
  faster, for a quick enough shutter. Any number of photos works; on long spirals the rig pauses for a moment now
  and then to reset the platform servo's turn count.
- **Measure** (under the plan) times a few quick sample shots to find how often your camera keeps up (an A6600
  shooting RAW + JPEG: about one a second). Moving Shots spaces the photos at least that far apart, never fires
  while the camera is still busy with the last one, and slows down (holding still at 4 photos waiting) if the
  camera falls behind anyway, so no photo is lost.
- **Preview** plays the capture in the 3D view first, without moving the rig.
- **Start**, **Pause**, **Stop**. The progress bar shows the photos done and about how long is left.
- **Take Photo** takes a single photo where the rig is.

Photos appear in the **Photos** card as they arrive. Click one to see it full size, **View All** for a grid. They're
saved in `~/Pictures/gosts-rig/`, a folder per batch named after its start time. **Reset** starts a new batch.

## Camera settings

The **gear** in the Camera card's title opens the camera's settings next to a sample shot, in a Settings card in place
of Photos and Rig View (✕, Esc or the gear again closes it; the Rig card's gear opens Rig Setup there the same way).

- Shutter, f-stop, ISO, white balance and focus, applied as you change them. **Take Sample Shot** shows how
  they look, or **Show Live Preview** shows the camera's live view as you change them (lower resolution: use a
  sample shot to check focus or use the gray card).
- **Detect** next to the f-stop finds the range your lens really has (about half a minute).
- Next to the f-stop: the **depth of field**, how deep the sharp zone is, for where the object sits on the rig.
- **Gray Card** (with white balance on a color temperature): drag a box over a gray card in the sample. It
  suggests the color temperature and color shifts that make the card neutral, a button each to apply.
- **Focus** (in Manual focus): drag a box over the part that matters to see it large. **Auto** focuses on that
  part for you; the arrows nudge the focus nearer or further. Focus stays where you leave it for the capture.

## Tips

- *"Another program has the camera"* (macOS): quit Photos and Image Capture, then run
  `killall ptpcamerad mscamerad-xpc`.
- Use **Manual** focus for captures: in an autofocus mode the camera won't fire until it has found focus.
- The camera's battery level is shown in the Camera card's title, next to the gear.

Settings are kept in `~/Library/Application Support/gosts-rig/config.toml` on macOS (`~/.config/gosts-rig/` on
Linux); `--config` picks another file.
