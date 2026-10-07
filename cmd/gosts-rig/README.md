# gosts-rig: photogrammetry rig

An example app of [Go ST Servo](../../README.md) (`github.com/frifox/gosts`). Its servos are set up and
calibrated with the servo console, [gosts-ctl](../gosts-ctl).

![gosts-rig: a moving-shots capture under way, the photos coming in and the rig drawn live](../../docs/gosts-rig.jpg)

`gosts-rig` drives a photogrammetry rig built from 2020 extrusions: a 600×500 mm base with a
600 mm post in the middle of each long side, an ST3215 on top of each post tilting a 600×450 mm frame
(camera on one 450 mm bar, counterweight on the other) for the camera's **elevation**, and a third
ST3215 turning the platform under the object for the **azimuth**.

```bash
go install github.com/frifox/gosts/cmd/gosts-rig@latest

gosts-rig                           # pick the board in the browser (http://localhost:8081)
gosts-rig -sim                      # simulated rig, no hardware needed
gosts-rig -sim -camera sim          # and a simulated camera
```

Set up and calibrate the servos with gosts-ctl first (IDs, tuning, and "Set 0° to" so elevation 0° is
the camera level with the object and azimuth 0° is the platform's front); the two programs can't use
the serial port at the same time. In gosts-rig, **Setup** assigns the roles (by default #10 elevation
leader, mirrored; #11 elevation follower; #12 azimuth); the Motion card sets the servos' speed (in °/s) and
acceleration (in °/s², kept in the servos' steps of about 8.8°/s²; lower acceleration is gentler on the frame). The rig's servos are always switched
to multi-turn when gosts-rig connects, so moves never take the long way round. Elevation moves go straight
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
Pause and Stop; Setup can disconnect either) and on the right a **Camera** card (its title shows the camera's battery level, read every minute: amber below
20%, red below 10%; a timeline of every photo
taken, newest on the left, during a capture too (as thumbnails the server shrinks, ~850 px wide: decoding a 24 MP photo for a tile stalled the Rig View), each first as a placeholder while it's taken and comes
over; hovering one shows a delete button, which takes it off the timeline (its files stay); click one to see it full size, ← → to step, Esc to close;
**Reset** (red; it asks first), which clears the timeline, **View All**, every photo in a grid over the whole page where a click
shows one full size (← → to step, Esc, ← All Photos or a click beside the photo back to the grid where it was left, ✕ to close), and **Take Photo**) above the **Rig View**, a live perspective view
of the rig on the right (a 3D model rendered with [three.js](https://threejs.org): 2020 extrusions, the
ST3215s, the Sony A6600 with its ring flash, and [#3DBenchy](https://www.3dbenchy.com) (CC0) as the
object; bundled in the binary, so no internet is needed), with
every planned shot turning with the platform and turning green once taken; **Preview** animates the
capture as the rig does it: the camera tilts while the turntable turns the object, the sphere of shots
and a fading trail with it, coloured by the camera's speed against the path's average (neutral at normal, amber to coral up to 2×, blue down to ½×),
with a speed key showing the speed now in °/s, and each axis' (elevation ↑↓, azimuth ↺↻) against the servos'
top speed (red past it: a preview plays faster). Each photo pops its dot and fires the ring flash lightly. A running capture looks the same but follows the real rig (telemetry, smoothed), each
shot's dot going as its photo is taken; when it ends the trail runs out and the dots return, taken ones green. The photos are spread as evenly as possible over the part of the sphere round the object the
camera can reach (the min/max elevation in Rig Setup, which keeps the frame clear of the posts), on a
golden-angle spiral, and taken in rows of similar elevation, each row in azimuth order with
alternating direction, so the arm moves little and the platform never unwinds a whole turn. With
**Moving Shots** (for a fast shutter) the rig doesn't stop: the same shots are taken along one smooth
rising spiral, the platform turning one way while the camera climbs: the rig follows the same smooth curve
the preview draws (goals streamed ~30 times a second just ahead of it, corrected for how far it trails, within
the servos' speed and with gentle acceleration), each photo taken as the path passes its shot. The platform servo's multi-turn goals reach only about ±7.5
turns from where its count started, so a long spiral goes in laps: as far as fits, then the rig stops for a moment
while the servo's turn count is reset where it is (its calibrate-middle re-references the count; its own zero is
put back straight after, so the angle reads as before; under a second), and the spiral carries on (any number of
photos: 400 make a 13-turn spiral, one reset). If a reset can't be confirmed, the platform unwinds instead (whole
turns back to the same angle, about 176°/s). If the spiral fits turning the other way from where the platform is
(the same shots, mirrored), it goes that way.
While a capture runs, the bar's text has the photos done, the time so far and about how long is left (by the
pace since the first photo; a pause doesn't count). When the last photo is taken the rig returns to 0°/0° the short way (less than a turn) and stops. A preview
only moves the view: it never moves the rig or uses the camera. Each photo is
taken with the connected camera, recording where the rig really is as the shutter goes.

A real camera is driven through gphoto2 (`brew install gphoto2`). Set it to RAW & JPEG (and, for a Sony in
PC Remote, save to PC+Camera): each photo's RAW stays on the camera's card and only the JPEG comes over USB
(much quicker; each matched to its photo by the camera's file number, so a JPEG left over from before
is kept but never shown as a new photo), saved in a folder per batch, `~/Pictures/gosts-rig/<yyyy-mm-dd hh.mm.ss>/` named after the batch's first photo
(`PhotoDir` in the config file changes the parent; a batch runs from the app's start, or **Reset**, which also
numbers the photos from 1 again), and shown on
the page. A camera on JPEG only works the same way; on RAW only no picture would come. On macOS the
system's camera service grabs a camera when it's plugged in (and may open Photos): if taking a photo says
another program has the camera, quit Photos and Image Capture and run `killall ptpcamerad mscamerad-xpc`.
**Settings** (in the Camera title, right of the battery, while a camera is connected) opens the camera's settings next to a sample shot: exposure mode (P, A, S,
M; shutter, f-stop and ISO hold in M), shutter speed, f-stop (all there are, until **Detect** next to it finds
the lens's: the camera steps the aperture to its limits and back, about half a minute), ISO, white balance
(with a **Color temperature (K)** field and slider, 2500–9900 K by 100, when it's on Choose Color Temperature) and its shifts, as on the camera's WB grid: **amber–blue** and **green–magenta** sliders, ±7 (A–B by halves, G–M by quarters; **Reset** beside each, or a double-click, puts it back to 0, the camera's default; e.g. a magenta cast colour temperature can't fix is set off towards G)
and focus mode, each change
sent to the camera at once; **Take Sample Shot** takes a photo (kept off the timeline, saved in the batch's
folder name with `-samples`)
and shows it with the settings it was taken at, to adjust from. Beside **F-stop**: the depth of field at the f-stop
chosen (it changes with it), focused on the object's middle (the turntable's middle, up half the object's height:
its distance from the camera's sensor from the rig's geometry), at the focal length the last photo was taken at
(EXIF: a zoom's, as set); "sharp" by Sony's standard for APS-C (0.02 mm blur), the tip also strict (2 pixels,
0.008 mm). **Focus** (beside the focus mode, in Manual, DMF
or AF-S): drag a rectangle around a part of the sample and it's shown large over the sample, from the full-size
photo, with its sharpness (the spread of its edges: higher is sharper, comparable for the same part). **Auto**: the
app focuses on that part: the camera's autofocus gets it close (where its focus area says; in Manual it switches
to AF-S for it and back, so the focus stays locked for the capture), then the app steps the lens, medium steps
then small, a sample after each, on while the part gets sharper and back a step when it doesn't, ending at the
sharpest it saw (about 10–15 samples, half a minute; click again to stop). The near/far steps (‹ ‹‹ ‹‹‹, › ›› ›››:
small to large, Manual and DMF) drive the lens by hand (the big ones don't come back to quite the same place);
after each a new sample is taken and the part shown again, ▲ sharper or ▼ softer, till Close. (In AF-S or DMF
the camera won't fire till it has focus: Manual, focused from here, is the steady choice for a capture.) **Gray Card** (right of the colour temperature slider, in Kelvin mode): drag a rectangle
inside a gray card in the sample, and the app works out what makes it neutral, from the card's average colour and
the settings the sample was taken with, a button each to set: **Temperature** (when the white balance was a known
one) or **Amber–blue** (the A–B shift: one or the other, so setting one greys out the other), and **Green–magenta**
(the G–M shift: what colour temperature can't fix); one that would change nothing is greyed out (a shift step moves a colour ratio by e^0.0275 a quarter step, measured
on the A6600); each new sample is measured again in the same rectangle (till Dismiss or a new one). The simulated
camera has the same settings, and its sample comes out brighter or darker with them, and tinted by its 4300 K,
slightly green light against its white balance and shifts.

gosts-rig keeps one gphoto2 shell open on the camera: firing the shutter takes about 1.1 s on the A6600 (nothing
downloaded), and the JPEGs are collected in between, each to its own photo, while the rig moves on. Stopping
shots and Take Photo wait for the shutter only; Moving Shots doesn't wait at all, its path paced so the shots
are no closer together than the camera can keep up with (firing plus downloading, measured as it goes, about
2 s), each shot's pose read as its shutter goes.

The **Rig Setup** card (below Motion; like every card it folds by its title, each window remembering which are folded) sets the rig's measurements for the 3D view (in mm, seen from above: base X/Y, post Z, swing
X/Y, the camera's offset along the arms (camera X) and up/down from the bar (camera Z, default 0), the turntable's height (turntable Z), the object's height (object height, the model is scaled to it); the defaults draw the rig to scale)
and the min/max elevation the camera may reach (default −45° to 80°).
The view has 3D, Front (from behind the camera, looking at the object), Side (along the tilt axis) and Top
presets.

Settings are kept in `gosts-rig/config.toml` in the user's config directory (`-config` to change; one left in `gosts/config.toml` from before the app's rename is copied over the first time).
