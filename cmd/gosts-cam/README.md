# gosts-cam

The Raspberry Pi side of the Arducam OwlSight 64MP (OV64A40, autofocus) for
gosts-rig: one Python process owns the camera through picamera2 and serves an
HTTP API on port 8090. gosts-rig on the Mac talks to it over USB.

## Hardware

- Raspberry Pi 5 (4 GB or more: a 64 MP still needs a few hundred MB of buffers),
  Raspberry Pi OS Trixie, 64-bit.
- The camera ribbon on **CAM1**, through a 15-to-22-pin FPC cable (the Pi 5's
  camera connectors are the small 22-pin kind).
- **USB-C to the Mac.** The Pi's USB-C port runs in USB gadget mode
  (rpi-usb-gadget, CDC-ECM): the Mac sees a USB Ethernet adapter, and the Pi is
  at `10.12.194.1` (or `gosts-cam.local`).
- Power: try the Mac's USB-C port first. If the Pi browns out (resets, or the
  lightning-bolt warning when shooting 64 MP), feed 5 V into the GPIO header
  (pins 2/4 and a ground) or put a powered hub between them.

## Install

Copy this folder to the Pi and run, on the Pi:

```bash
sudo ./install.sh
```

It installs `python3-picamera2` and `rpi-usb-gadget`, turns gadget mode on,
adds `camera_auto_detect=0` and `dtoverlay=ov64a40,link-frequency=456000000`
under `[all]` in `/boot/firmware/config.txt` (commenting out
`camera_auto_detect=1`; the original is kept as `config.txt.gosts-cam.bak`),
copies the service to `/opt/gosts-cam`, enables `gosts-cam.service` and names
the Pi `gosts-cam`. Running it again changes nothing. Reboot afterwards.

## Checks

From the Mac, once the Pi has booted with USB-C plugged in:

```bash
ssh pi@10.12.194.1
```

On the Pi, the camera is there:

```bash
rpicam-still --list-cameras
```

From the Mac, the service answers:

```bash
curl 10.12.194.1:8090/info
```

The service's log: `journalctl -u gosts-cam -f` on the Pi.

## API

JSON unless stated.

| Request | Answer |
|---|---|
| `GET /info` | `{"model":"Arducam OwlSight 64MP","sensor":"ov64a40","version":"1","modes":[[4624,3472],[9248,6944]]}` |
| `POST /shoot` | `application/x-ndjson`, a line at a time: `{"event":"fired","at":"<RFC 3339>"}` when the frame is captured, then `{"event":"saved","files":["owl_00012.jpg","owl_00012.dng"],"meta":{"ExposureTime":µs,"AnalogueGain":g,"LensPosition":d,"ColourGains":[r,b]}}`, or `{"event":"error","error":"..."}`. The JPEG is always first. One shot at a time. |
| `GET /files/<name>` | the file's bytes; `DELETE` removes it (204) |
| `GET /preview` | `image/jpeg`, the current preview frame |
| `GET /settings` | `[{"key","label","current","choices":[...]}` or `{"key","label","current","range":true,"min","max","step","unit"}, ...]` |
| `POST /settings` | `{"key","value"}` → 204, or 400 `{"error"}` |
| `POST /focus/auto` | one autofocus cycle → `{"ok":true,"lensPosition":2.1,"status":"Focus Locked"}` (or `"No focus"`) |
| `POST /focus/nudge?steps=n` | n from −7 to 7, not 0: the lens n × 0.25 diopters nearer (+) or farther (−) → `{"lensPosition":d}` |

Settings (`current` and values are strings):

| Key | Values |
|---|---|
| `focus` | `Manual`, `Automatic` (focuses before each shot, like a half press) |
| `mode` | `P` (auto exposure), `M` |
| `shutter` | `1/4000` … `1/2`, `1`, `2`, `4` (used in M) |
| `iso` | `100`, `200`, `400`, `800`, `1600` (analog gain = ISO/100; used in M) |
| `whitebalance` | `Auto`, `Daylight`, `Cloudy`, `Tungsten`, `Fluorescent`, `Indoor`, `Choose Color Temperature` (when libcamera has `ColourTemperature`) |
| `colortemp` | 2500–10000 K, step 100; listed only while white balance is `Choose Color Temperature` |
| `resolution` | `16 MP` (4624×3472), `64 MP` (9248×6944) |
| `format` | `RAW + JPEG` (JPEG and DNG), `JPEG` |

Photos are named `owl_NNNNN.jpg`/`.dng` and kept in `/var/tmp/gosts-cam`
(`--dir`) until deleted; the settings, the photo counter and the lens position
are in `settings.json` there.

## Trying it on a Mac

`--fake` needs no camera and no picamera2; photos and the preview are a test
pattern (drawn with Pillow when it's installed, else a small fixed JPEG):

```bash
python3 gosts_cam.py --fake --port 8090 --dir /tmp/gosts-cam
```

```bash
curl -N -X POST localhost:8090/shoot
```
