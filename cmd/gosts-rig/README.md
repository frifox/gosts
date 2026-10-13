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
gosts-rig --photos ~/Pictures/gosts-rig-sim # the captures saved elsewhere (or PhotoDir in the config)
```

## First time

1. Set up the servos with [gosts-ctl](../gosts-ctl) first: IDs, tuning, and zero (elevation 0° = camera level with
   the object, azimuth 0° = the platform's front). Close it after: only one program can use the serial port.
   Once the rig is connected, the same console is in gosts-rig too: the **gear** in the Rig card's title opens it as
   **Servo Settings**, the right side's only card, on the rig's board, with gosts-ctl's settings (names, groups, …).
2. In gosts-rig, the servos' roles come from Servo Settings: make a group of the two elevation servos (leader first)
   and the rig takes it as the elevation, the other servo as the azimuth (by default #1 and #2 elevation, #3 azimuth).
3. The rig itself is fixed: the base 600 × 500 mm, 400 mm side posts, the swing 600 × 450 mm, a 100 mm turntable and object, the turntable as high as puts the object's middle on the tilt axis, and the camera's sensor 335 mm out with its lens in line with it. The Rig card's two dials, elevation and azimuth, show where the rig is (the needle) and where it's going (the dot); click or drag on one to move there, or **Go 0°** below it; beside that, each axis' **Torque Lock** (off: its servos let go, to move by hand). The chevron beside the elevation's Go 0° shows (or hides) **Set Min** and **Set Max**, which save where the camera is now as its lowest or highest elevation. The ✕ beside them clears both (the whole −90° to 90°). The elevation's out-of-limits angles are shaded red: a move there stops at the nearest limit.
4. Set the camera to **RAW & JPEG** (on a Sony in PC Remote: save to PC+Camera). The RAWs stay on the card,
   and only the JPEGs come over USB.

## Taking photos

- In the **Camera** card, choose how many photos you want. They're spread evenly round the object, as high and low as the
  camera may go.
- **Path**: **Rings** (the default) takes the photos in rings, the camera's height changing only between rings
  (fewer photos in the higher rings): best for photogrammetry (RealityScan, Metashape, Apple Object Capture).
  **Sphere** spreads them as evenly as can be round the object: best for Gaussian splatting and NeRF.
- **Rig motion**: **Continuous** doesn't stop: the rig
  glides along one smooth path, taking each photo as it passes, which is much faster, for a quick enough shutter. Any
  number of photos works; on long spirals the rig pauses for a moment now and then to reset the platform servo's turn
  count. **Stop & Shoot** stops at each shot and waits the settle time before the photo, and after it keeps still
  while the shutter's open (the camera's shutter speed, read as the capture starts, and 0.1 s to spare from 1/15 s on).
- **Speed** (Rig card), set one of two ways; the ⇄ beside it swaps (each keeps its value):
  **s/pic**, how long a capture takes a photo (Continuous: from one photo to the next, at least the camera's pace;
  Stop & Shoot: each move, settle and photo together; the servos go as fast as that takes), or **°/s**, the servos'
  speed. A change during a Continuous capture takes at once (a new path). Moves by hand (the dials, Go 0°) go at
  the °/s speed either way.
- **Measure** (under the choices) times a few quick sample shots to find how often your camera keeps up (an A6600
  shooting RAW + JPEG: about one every 1.2 s). Continuous spaces the photos at least that far apart, never fires
  while the camera is still busy with the last one, and slows down (holding still at 4 photos waiting) if the
  camera falls behind anyway, so no photo is lost.
- **Capture alignment data for** (above the buttons; None writes nothing, All every format below): when a capture ends, writes where each photo was taken from
  (the rig's poses, as starting points the app refines) beside the photos, for: Agisoft Metashape
  (`alignment-metashape.csv`: File → Import → Import Reference), RealityScan / RealityCapture (an `.xmp` per
  photo), Nerfstudio / Gaussian splatting (`transforms.json`), COLMAP (a text model in `colmap/`), or Apple Object
  Capture (`gravity.json`: Apple takes no poses, only which way is down, through a tool that passes it
  on). Works best with a plain backdrop (the object turns, not the camera).
- **Simulate** plays the capture in the 3D view first, without moving the rig.
- **Capture** takes the photos: while it runs, it turns into **Stop** and Preview into **Pause** (Resume). The
  progress bar shows the photos done, the time so far and about how long is left (and before you start, an
  estimate of the whole capture: from the moves at the rig's speed, the settle time and the camera's measured pace).
- **Take Photo** takes a single photo where the rig is.

Photos appear in the **Photos** card as they arrive. Click one to see it full size (drag to move it, the wheel or the
navigator at the bottom right to zoom, up to 200% of its own pixels), **Gallery** for a grid. They're
saved in `~/Pictures/gosts-rig/`, a folder per capture named after its start time (its name beside **New Capture**;
the pencil after it renames it, as the pencil by a folder's name in **Browse** does),
sample shots in its `samples` folder, so they go with it. Each start of the app is a new capture (its folder made with
its first photo or sample). **New Capture** starts a new one once something's been taken (a photo or a sample shot): a new folder, made then (empty, ready), and
the timeline cleared (the photos taken stay saved; **Browse** has them; it asks first if there are any).
**Browse** opens the captures saved there (sample shots left out), newest first, each with its middle photo,
name and count, as thumbnails or a list: open one to see its photos (← → to step through them large); the pencil on a
capture (on hover) renames its folder, the bin beside it deletes it (asks first). Deleting a photo (the bin on it, on the
timeline or in a folder) or a capture moves its files to the Trash (a photo's RAW too), from where the Finder can
still bring them back.

### Build Model (Apple Object Capture)

In a capture opened in Browse, **Build Model** (the middle of its bar) builds a 3D model from its photos with Apple's
Object Capture, on this Mac (macOS 14 or later, Apple silicon), through
[`cmd/apple-obj-cap`](../apple-obj-cap/main.swift) (a small Swift tool; gosts-rig builds it with `swiftc` the first
time, from the source tree). The panel has all of Object Capture's options: OBJ or USDZ; the detail (Preview, a quick
look, to Raw, or Custom: the most polygons, texture size, maps and format); the photos' order (Sequential for the rig's
rings); feature sensitivity; triangles or quads; masking out the background; building all that's seen; checkpoints;
the rig's gravity (the capture's `gravity.json`: pick Apple Object Capture, or All, in Capture alignment data for); a
bounding box (`bounds.json`) and a point cloud (`pointcloud.ply`, both in `model-build`) too; the output folder (in the capture's:
`model-<detail>`, `model-full` say, following the detail till you give it a name of your own). **Start Process** saves them and builds: a bar with its stage, and a
mark on each photo: ✓ used, ✕ rejected (why, on hover), ↷ skipped, ? not placed in the model (none till it's known). The model goes to the output folder (Object Capture writes only
into an empty one: a model built there before goes to the Trash; a folder with other files in it is refused), the rest to `model-build` (the options, checkpoints, each photo's pose, how each
fared: the marks are there when the capture's opened again). One build at a time; **Cancel build** stops it.

A capture's models (its folders with an OBJ or a USDZ in them: Build Model's, or put there by hand) come first in its
gallery, a tile each with a picture of the model (rendered in the page the first time, with three.js, and kept in the
cache folder). A click shows it over the page in 3D: drag to turn it, right-drag (or ⇧ drag) to move it, the wheel to
zoom (at the pointer), F for the whole model again; Textured, Clay (its shape, gray) or Wireframe (its triangles); its
triangles and size (cm) in the bar. **Measure**: click two points on the model, the distance between them (and its X, Y,
Z parts) shows; a third click starts again, Measure again clears it. **Open in App** opens it in the Mac's app for it, to edit it (Preview, unless
another's set: Blender, say, in the Finder's Get Info); **Show in Finder** shows its folder. A USDZ isn't shown in the
page (three.js can't read Apple's), only opened. Its tile's bin moves the model's folder to the Trash.

The view buttons at the top left glide to a view, in Blender's and Fusion's axes (X red, Y green, Z blue up): **Front**
(X right, Z up, Y away), **Side** (Y right, Z up, X towards you), **Top** (Y right, X down, Z towards you) and **3D**
(Z up, X right and away, Y left and away). **Edit** (an OBJ) readies a model for Blender, Fusion 360 and the like: upright, square to the axes, at its real size.
The viewer shows the axes as they do (X red, Y green, Z blue up) and the ground grid at the origin. **Pick Points**,
then click on the model (each click snaps to the nearest corner of the triangle under it):

1. **Level**: 3 or more points on what it should stand on; the plane through them goes to the ground, the model above it.
2. **Align**: 2 or more points along an edge, and X or Y; the model turns about the vertical so the edge runs along it.
3. **Scale**: 2 points and their real distance (mm or in); the model scales to match (Object Capture's own size is a
   guess from the photos).
4. **Rotate**: a quarter turn (−90° or +90°) about X, Y or Z through the origin (nothing else moved).
5. **Trim**: move the sides of a box (X, Y, Z, each min and max) in; the part outside it shows cut off as you go, and
   **Trim** cuts it away: triangles across a side are cut along it (their texture and normals kept), so the cut is
   exact.
6. **Origin**: a point, and a plane (XY, XZ or YZ); the model slides in that plane so the point is over the origin (**Center**), the
   third axis left as it is (XY: a point at the top's middle over the origin, its height kept).

Each step centers it on the origin; the size (X × Y × Z) shows as it goes; **Undo** steps back. **Save** writes it as a
new model in the capture (named `<model>-aligned` unless you name it): the OBJ in the file units picked (mm, the
default; in; or m, Blender's), y up as OBJ files are (Blender and Fusion turn it z up), its materials and textures
copied; **Also an STL** (z up, as STL files are); **Simplify to** a number of triangles (meshoptimizer; the texture
kept). Its `edit.json` says what it was made from and how; saving under the same name again replaces it (the one
before to the Trash). **Overwrite** (by the name) saves over the model being edited itself: its folder gets the edited
model, the one there before goes to the Trash.

## Camera settings

The **gear** in the Camera card's title opens the camera's settings next to a sample shot, in a Settings card in place
of Photos and Rig View (✕, Esc or the gear again closes it; the Rig card's gear opens Servo Settings the same way, in place of both).

- Zoom (on a camera that zooms by remote, with a power zoom lens; the A6600 doesn't: zoom on the camera), focus,
  shutter, f-stop, ISO and white balance, applied as
  you change them. **Show Live Preview** shows the camera's live view as you change them, or **Take Sample Shot**
  how they look (the live view is lower resolution: use a sample shot to check focus or use the gray card). The zoom
  can't change during a capture; its focal length goes into the alignment data for the apps that take one
  (RealityScan, Nerfstudio, COLMAP).
- **Pause for X minutes after Y shots** (below the buttons): a flash that gets hot gets a rest. During a capture,
  after every Y photos it pauses X minutes (the note counts down), then carries on by itself; **Resume** carries on
  sooner. The estimate includes the pauses. Below it, the Godox AR400's overheat protection (its manual): how many
  continuous flashes at each power set it off (40 at full power, up to 500 at 1/128). Click the flash's power, and
  pick **Safe**, **Acceptable** or **Risky** below it (60%, 80% or all of the limit between pauses, resting 15,
  10 or 5 minutes): the pause is set to the line's Pause column as it reads (a plan that fits in one go never
  reaches it; a bigger one later does), and the note says how many pauses the plan's photos make.
- **Gray Card** (with white balance on a color temperature): drag a box over a gray card in the sample. It
  suggests the color temperature and color shifts that make the card neutral, a button each to apply.
- **Focus** (in Manual focus) offers two ways, then **Start**:
  - **Single Point**: drag a box over the part that matters to see it large. **Auto** focuses on that part for you;
    the arrows nudge the focus nearer or further.
  - **Near/Far Points**: put two printed contrast cards where the object comes nearest and farthest over a full
    turn, then drag a box round the near card and one round the far card. **Focus between** steps the lens through
    both cards' sharpest and leaves it where they're equally sharp: the depth of field centered between them. If the
    depth of field doesn't reach both cards, it says so: stop down (a higher f-number) or zoom out, and try again.

  Focus stays where you leave it for the capture.

## Tips

- *"Another program has the camera"* (macOS): quit Photos and Image Capture, then run
  `killall ptpcamerad mscamerad-xpc`.
- Use **Manual** focus for captures: in an autofocus mode the camera won't fire until it has found focus.
- The camera's battery level is shown in the Camera card's title, next to the gear.

Settings are kept in `~/Library/Application Support/gosts-rig/config.toml` on macOS (`~/.config/gosts-rig/` on
Linux); `--config` picks another file.
