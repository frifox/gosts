#!/usr/bin/env python3
"""gosts-cam: the Arducam OwlSight 64MP (OV64A40) on a Raspberry Pi 5, over HTTP.

One process owns the camera (picamera2) and serves gosts-rig on the Mac:
a preview configuration keeps running; a photo switches to the still
configuration for one frame (switch_mode_and_capture_request), saves the
JPEG (and DNG if RAW is on) to a scratch folder, and the preview resumes.

    python3 gosts_cam.py [--port 8090] [--dir /var/tmp/gosts-cam] [--fake]

--fake needs no camera (and no picamera2): photos and the preview are a test
pattern, for trying the API on a Mac. Only the standard library is used;
Pillow draws a nicer pattern when it's there.

API (see README.md):
    GET  /info                 model, sensor, version, still sizes
    POST /shoot                NDJSON: {"event":"fired"}, then "saved" or "error"
    GET  /files/<name>         a photo's bytes; DELETE removes it
    GET  /preview              a JPEG of the current preview frame
    GET  /settings             the settings and their choices or ranges
    POST /settings             {"key","value"} -> 204, or 400 {"error"}
    POST /focus/auto           one autofocus cycle
    POST /focus/nudge?steps=n  the lens n quarter diopters nearer (+) or farther (-)
"""

import argparse
import base64
import datetime
import io
import json
import os
import re
import signal
import sys
import threading
import time
import traceback
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, unquote, urlparse

VERSION = "1"
MODEL = "Arducam OwlSight 64MP"
SENSOR = "ov64a40"

# Still sizes by the resolution setting's value.
RESOLUTIONS = {"16 MP": (4624, 3472), "64 MP": (9248, 6944)}
PREVIEW_SIZE = (1280, 960)

FOCUS = ["Manual", "Automatic"]
MODES = ["P", "M"]
SHUTTERS = ["1/4000", "1/2000", "1/1000", "1/500", "1/250", "1/125", "1/60",
            "1/30", "1/15", "1/8", "1/4", "1/2", "1", "2", "4"]
ISOS = ["100", "200", "400", "800", "1600"]
WB_PRESETS = ["Auto", "Daylight", "Cloudy", "Tungsten", "Fluorescent", "Indoor"]
WB_TEMP = "Choose Color Temperature"
FORMATS = ["RAW + JPEG", "JPEG"]
CT_MIN, CT_MAX, CT_STEP = 2500, 10000, 100
NUDGE_DIOPTERS = 0.25
JPEG_QUALITY = 93

DEFAULTS = {
    "focus": "Manual",
    "mode": "P",
    "shutter": "1/60",
    "iso": "100",
    "whitebalance": "Auto",
    "colortemp": "5500",
    "resolution": "16 MP",
    "format": "RAW + JPEG",
}

LABELS = {
    "focus": "Focus mode",
    "mode": "Exposure mode",
    "shutter": "Shutter speed",
    "iso": "ISO",
    "whitebalance": "White balance",
    "colortemp": "Color temperature",
    "resolution": "Resolution",
    "format": "File format",
}

FILE_NAME = re.compile(r"^owl_\d{5,}\.(jpg|dng)$")


def shutter_us(s):
    """A shutter speed ("1/60", "2") in microseconds."""
    if "/" in s:
        a, b = s.split("/")
        return int(round(1e6 * int(a) / int(b)))
    return int(round(1e6 * float(s)))


def rfc3339(t):
    """A Unix time (s) as RFC 3339 with fractional seconds, UTC."""
    d = datetime.datetime.fromtimestamp(t, datetime.timezone.utc)
    return d.isoformat(timespec="microseconds").replace("+00:00", "Z")


def log(*a):
    print(*a, file=sys.stderr, flush=True)


# ---------------------------------------------------------------------------
# Persistent state: the settings, the photo counter, the lens position.

class State:
    def __init__(self, folder):
        self.path = os.path.join(folder, "settings.json")
        self.settings = dict(DEFAULTS)
        self.counter = 0
        self.lens = None  # diopters; None: the camera's default
        try:
            with open(self.path) as f:
                d = json.load(f)
            for k, v in d.get("settings", {}).items():
                if k in DEFAULTS:
                    self.settings[k] = str(v)
            self.counter = int(d.get("counter", 0))
            if d.get("lens") is not None:
                self.lens = float(d["lens"])
        except FileNotFoundError:
            pass
        except Exception as e:
            log("settings: %s unreadable (%s), defaults" % (self.path, e))

    def save(self):
        tmp = self.path + ".tmp"
        with open(tmp, "w") as f:
            json.dump({"settings": self.settings, "counter": self.counter, "lens": self.lens}, f, indent=1)
        os.replace(tmp, self.path)


# ---------------------------------------------------------------------------
# The camera: picamera2, or a fake.

class PiCamera:
    """The OwlSight through picamera2."""

    def __init__(self):
        from picamera2 import Picamera2
        from libcamera import controls
        self.C = controls
        self.cam = Picamera2()
        self.cam.options["quality"] = JPEG_QUALITY
        self.ctrls = self.cam.camera_controls  # name -> (min, max, default)
        self.preview = self.cam.create_preview_configuration(main={"size": PREVIEW_SIZE, "format": "RGB888"})
        self.base = dict(self.preview.get("controls", {}))
        self.cam.configure(self.preview)
        self.cam.start()
        self.new_ae = "ExposureTimeMode" in self.ctrls  # libcamera 0.5+: AeEnable became these

    def has_color_temp(self):
        return "ColourTemperature" in self.ctrls

    def lens_limits(self):
        lo, hi, default = self.ctrls.get("LensPosition", (0.0, 15.0, 1.0))
        return float(lo), float(hi), float(default if default is not None else 1.0)

    def controls(self, s, lens, still=False):
        """libcamera controls for settings s, the lens at lens (Manual focus, or a still)."""
        C = self.C
        c = {}
        if s["focus"] == "Manual" or still:
            c["AfMode"] = C.AfModeEnum.Manual
            c["LensPosition"] = lens
        else:
            c["AfMode"] = C.AfModeEnum.Auto
        if s["mode"] == "M":
            exp = shutter_us(s["shutter"])
            lo, hi, _ = self.ctrls.get("ExposureTime", (1, 4000000, None))
            exp = max(int(lo), min(int(hi), exp))
            if self.new_ae:
                c["ExposureTimeMode"] = C.ExposureTimeModeEnum.Manual
                c["AnalogueGainMode"] = C.AnalogueGainModeEnum.Manual
            else:
                c["AeEnable"] = False
            c["ExposureTime"] = exp
            c["AnalogueGain"] = int(s["iso"]) / 100.0
            if not still:  # let the preview's frames be as long as the exposure
                fd = self.base.get("FrameDurationLimits", (100, 83333))
                c["FrameDurationLimits"] = (fd[0], max(fd[1], exp + 1000))
        else:
            if self.new_ae:
                c["ExposureTimeMode"] = C.ExposureTimeModeEnum.Auto
                c["AnalogueGainMode"] = C.AnalogueGainModeEnum.Auto
            else:
                c["AeEnable"] = True
                c["ExposureTime"] = 0  # 0: not fixed
                c["AnalogueGain"] = 0
            if not still and "FrameDurationLimits" in self.base:
                c["FrameDurationLimits"] = self.base["FrameDurationLimits"]
        if s["whitebalance"] == WB_TEMP and self.has_color_temp():
            c["AwbEnable"] = False
            c["ColourTemperature"] = int(s["colortemp"])
        else:
            mode = s["whitebalance"] if s["whitebalance"] in WB_PRESETS else "Auto"
            c["AwbEnable"] = True
            c["AwbMode"] = getattr(C.AwbModeEnum, mode)
        return c

    def apply(self, s, lens):
        c = self.controls(s, lens)
        # Kept in the preview configuration too, so they come back after a still.
        self.preview["controls"] = {**self.base, **c}
        self.cam.set_controls(c)

    def capture(self, s, lens, size, raw, jpg, dng, fired):
        cfg = self.cam.create_still_configuration(
            main={"size": size}, raw={"size": size}, buffer_count=1,
            controls=self.controls(s, lens, still=True))
        req = self.cam.switch_mode_and_capture_request(cfg)
        try:
            md = req.get_metadata()
            at = time.time()
            ts = md.get("SensorTimestamp")
            if ts and hasattr(time, "CLOCK_BOOTTIME"):  # when the frame was, not when it got here
                at -= (time.clock_gettime_ns(time.CLOCK_BOOTTIME) - ts) / 1e9
            fired(at)
            req.save("main", jpg)
            if raw:
                req.save_dng(dng)
        finally:
            req.release()
        return {
            "ExposureTime": md.get("ExposureTime"),
            "AnalogueGain": md.get("AnalogueGain"),
            "LensPosition": md.get("LensPosition", lens),
            "ColourGains": list(md.get("ColourGains", ())),
        }

    def preview_jpeg(self):
        img = self.cam.capture_image("main")
        buf = io.BytesIO()
        img.save(buf, "JPEG", quality=80)
        return buf.getvalue()

    def autofocus(self):
        ok = bool(self.cam.autofocus_cycle())
        lens = self.cam.capture_metadata().get("LensPosition")
        return ok, lens

    def set_lens(self, lens):
        c = {"AfMode": self.C.AfModeEnum.Manual, "LensPosition": lens}
        self.preview["controls"].update(c)
        self.cam.set_controls(c)

    def close(self):
        try:
            self.cam.stop()
            self.cam.close()
        except Exception:
            pass


# A 160x120 color-bar JPEG, for --fake without Pillow.
FAKE_JPEG_B64 = """
/9j/4AAQSkZJRgABAQAASABIAAD/4QBMRXhpZgAATU0AKgAAAAgAAYdpAAQAAAABAAAAGgAAAAAA
A6ABAAMAAAABAAEAAKACAAQAAAABAAAAoKADAAQAAAABAAAAeAAAAAD/7QA4UGhvdG9zaG9wIDMu
MAA4QklNBAQAAAAAAAA4QklNBCUAAAAAABDUHYzZjwCyBOmACZjs+EJ+/8AAEQgAeACgAwEiAAIR
AQMRAf/EAB8AAAEFAQEBAQEBAAAAAAAAAAABAgMEBQYHCAkKC//EALUQAAIBAwMCBAMFBQQEAAAB
fQECAwAEEQUSITFBBhNRYQcicRQygZGhCCNCscEVUtHwJDNicoIJChYXGBkaJSYnKCkqNDU2Nzg5
OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6g4SFhoeIiYqSk5SVlpeYmZqio6Slpqeo
qaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2drh4uPk5ebn6Onq8fLz9PX29/j5+v/EAB8BAAMB
AQEBAQEBAQEAAAAAAAABAgMEBQYHCAkKC//EALURAAIBAgQEAwQHBQQEAAECdwABAgMRBAUhMQYS
QVEHYXETIjKBCBRCkaGxwQkjM1LwFWJy0QoWJDThJfEXGBkaJicoKSo1Njc4OTpDREVGR0hJSlNU
VVZXWFlaY2RlZmdoaWpzdHV2d3h5eoKDhIWGh4iJipKTlJWWl5iZmqKjpKWmp6ipqrKztLW2t7i5
usLDxMXGx8jJytLT1NXW19jZ2uLj5OXm5+jp6vLz9PX29/j5+v/bAEMAAgICAgICBAICBAYEBAQG
CAYGBgYICggICAgICgwKCgoKCgoMDAwMDAwMDA4ODg4ODhAQEBAQEhISEhISEhISEv/bAEMBAwMD
BQQFCAQECBMNCw0TExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTExMT
ExMTE//dAAQACv/aAAwDAQACEQMRAD8A/ayiiigDjfFv/Lv/AMD/APZa42uy8W/8u/8AwP8A9lrj
a/iXxd/5KjGf9uf+m4H0mA/gR+f5hXy/X1BXy/X719E//mb/APcD/wBzH8xfSZ/5lf8A3G/9xBXG
+Lf+Xf8A4H/7LXZVxvi3/l3/AOB/+y1+8+L3/JLYz/tz/wBOQP5hwH8aPz/I42iiiv4kPoj5jooo
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
"""
FAKE_JPEG = base64.b64decode("".join(FAKE_JPEG_B64.split()))


class FakeCamera:
    """No camera: a test pattern, plausible metadata."""

    def __init__(self):
        try:
            from PIL import Image, ImageDraw
            self.pil = (Image, ImageDraw)
        except ImportError:
            self.pil = None
        self.lens = 1.0

    def has_color_temp(self):
        return True

    def lens_limits(self):
        return 0.0, 15.0, 1.0

    def apply(self, s, lens):
        self.lens = lens

    def pattern(self, w, h, text):
        if not self.pil:
            return FAKE_JPEG
        Image, ImageDraw = self.pil
        img = Image.new("RGB", (w, h))
        d = ImageDraw.Draw(img)
        bars = [(235, 235, 235), (235, 235, 16), (16, 235, 235), (16, 235, 16),
                (235, 16, 235), (235, 16, 16), (16, 16, 235), (16, 16, 16)]
        for i, c in enumerate(bars):
            d.rectangle([i * w // 8, 0, (i + 1) * w // 8, h * 3 // 4], fill=c)
        for x in range(w):
            g = x * 255 // max(1, w - 1)
            d.line([x, h * 3 // 4, x, h], fill=(g, g, g))
        d.text((10, 10), text, fill=(0, 0, 0))
        buf = io.BytesIO()
        img.save(buf, "JPEG", quality=85)
        return buf.getvalue()

    def capture(self, s, lens, size, raw, jpg, dng, fired):
        exp = shutter_us(s["shutter"]) if s["mode"] == "M" else 16667
        time.sleep(0.2 + exp / 1e6)
        fired(time.time())
        time.sleep(0.3)  # encoding
        with open(jpg, "wb") as f:
            f.write(self.pattern(size[0] // 4, size[1] // 4, "%s %s" % (os.path.basename(jpg), rfc3339(time.time()))))
        if raw:
            with open(dng, "wb") as f:
                f.write(b"II*\x00" + b"\x00" * 60)  # not a real DNG
        gain = int(s["iso"]) / 100.0 if s["mode"] == "M" else 1.5
        return {"ExposureTime": exp, "AnalogueGain": gain, "LensPosition": lens, "ColourGains": [1.8, 1.6]}

    def preview_jpeg(self):
        return self.pattern(640, 480, "preview " + rfc3339(time.time()))

    def autofocus(self):
        time.sleep(0.5)
        self.lens = 2.0
        return True, self.lens

    def set_lens(self, lens):
        self.lens = lens

    def close(self):
        pass


# ---------------------------------------------------------------------------
# The service: settings, shooting, focus.

class BadRequest(Exception):
    pass


class Service:
    def __init__(self, cam, folder):
        self.cam = cam
        self.folder = folder
        self.state = State(folder)
        self.cam_lock = threading.Lock()   # one camera operation at a time
        self.shoot_lock = threading.Lock()  # one shot at a time
        lo, hi, default = cam.lens_limits()
        self.lens_lo, self.lens_hi = lo, hi
        if self.state.lens is None:
            self.state.lens = default
        if self.state.settings["whitebalance"] not in self.wb_choices():
            self.state.settings["whitebalance"] = "Auto"
        with self.cam_lock:
            self.cam.apply(self.state.settings, self.state.lens)

    def wb_choices(self):
        return WB_PRESETS + ([WB_TEMP] if self.cam.has_color_temp() else [])

    def choices(self, key):
        return {
            "focus": FOCUS, "mode": MODES, "shutter": SHUTTERS, "iso": ISOS,
            "whitebalance": self.wb_choices(), "resolution": list(RESOLUTIONS), "format": FORMATS,
        }[key]

    def settings(self):
        s = self.state.settings
        out = []
        for key in ["focus", "mode", "shutter", "iso", "whitebalance", "colortemp", "resolution", "format"]:
            if key == "colortemp":
                if s["whitebalance"] == WB_TEMP:
                    out.append({"key": key, "label": LABELS[key], "current": s[key], "range": True,
                                "min": CT_MIN, "max": CT_MAX, "step": CT_STEP, "unit": "K"})
                continue
            out.append({"key": key, "label": LABELS[key], "current": s[key], "choices": self.choices(key)})
        return out

    def set(self, key, value):
        if not isinstance(key, str) or key not in DEFAULTS:
            raise BadRequest("unknown setting %r" % (key,))
        if isinstance(value, bool) or value is None:
            raise BadRequest("bad value for %s" % key)
        if isinstance(value, float) and value.is_integer():
            value = int(value)
        value = str(value).strip()
        if key == "colortemp":
            try:
                k = int(float(value.rstrip("Kk").strip()))
            except ValueError:
                raise BadRequest("colortemp: %r is not a number" % value)
            if not CT_MIN <= k <= CT_MAX:
                raise BadRequest("colortemp: %d is not within %d-%d" % (k, CT_MIN, CT_MAX))
            value = str(int(round(k / CT_STEP)) * CT_STEP)
        elif value not in self.choices(key):
            raise BadRequest("%s: %r is not one of %s" % (key, value, ", ".join(self.choices(key))))
        with self.cam_lock:
            self.state.settings[key] = value
            self.state.save()
            self.cam.apply(self.state.settings, self.state.lens)

    def next_name(self):
        while True:
            self.state.counter += 1
            name = "owl_%05d" % self.state.counter
            if not any(os.path.exists(os.path.join(self.folder, name + ext)) for ext in (".jpg", ".dng")):
                self.state.save()
                return name

    def shoot(self, emit):
        if not self.shoot_lock.acquire(timeout=60):
            emit({"event": "error", "error": "busy: another shot is still being taken"})
            return
        try:
            with self.cam_lock:
                s = dict(self.state.settings)
                if s["focus"] == "Automatic":  # like a half press: focus, then shoot there
                    ok, lens = self.cam.autofocus()
                    if lens is not None:
                        self.state.lens = float(lens)
                raw = s["format"] == "RAW + JPEG"
                name = self.next_name()
                jpg = os.path.join(self.folder, name + ".jpg")
                dng = os.path.join(self.folder, name + ".dng")
                fired_at = []

                def fired(at):
                    fired_at.append(at)
                    emit({"event": "fired", "at": rfc3339(at)})

                try:
                    meta = self.cam.capture(s, self.state.lens, RESOLUTIONS[s["resolution"]], raw, jpg, dng, fired)
                finally:
                    try:  # back to the preview's controls
                        self.cam.apply(self.state.settings, self.state.lens)
                    except Exception as e:
                        log("shoot: restoring the preview controls: %s" % e)
            files = [name + ".jpg"] + ([name + ".dng"] if raw else [])
            meta = {k: (round(v, 4) if isinstance(v, float) else v) for k, v in meta.items()}
            meta["ColourGains"] = [round(float(g), 4) for g in meta.get("ColourGains") or []]
            emit({"event": "saved", "files": files, "meta": meta})
        except Exception as e:
            log("shoot: %s" % traceback.format_exc())
            emit({"event": "error", "error": str(e) or type(e).__name__})
        finally:
            self.shoot_lock.release()

    def preview(self):
        with self.cam_lock:
            return self.cam.preview_jpeg()

    def focus_auto(self):
        with self.cam_lock:
            ok, lens = self.cam.autofocus()
            if lens is not None:
                self.state.lens = float(lens)
                self.state.save()
            if self.state.settings["focus"] == "Manual":  # stay where it focused
                self.cam.set_lens(self.state.lens)
            return {"ok": ok, "lensPosition": round(self.state.lens, 4),
                    "status": "Focus Locked" if ok else "No focus"}

    def nudge(self, steps):
        with self.cam_lock:
            lens = self.state.lens + steps * NUDGE_DIOPTERS
            self.state.lens = max(self.lens_lo, min(self.lens_hi, lens))
            self.state.save()
            self.cam.set_lens(self.state.lens)
            return {"lensPosition": round(self.state.lens, 4)}

    def close(self):
        with self.cam_lock:
            self.cam.close()


# ---------------------------------------------------------------------------
# HTTP

class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "gosts-cam/" + VERSION
    service = None  # set in main

    def log_message(self, fmt, *args):
        if self.path.startswith("/preview"):
            return
        log("%s %s" % (self.address_string(), fmt % args))

    def send_json(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def send_empty(self, code=204):
        self.send_response(code)
        self.end_headers()

    def body_json(self):
        n = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(n) if n else b""
        try:
            return json.loads(raw or b"{}")
        except ValueError:
            raise BadRequest("the body is not JSON")

    def run(self, fn):
        try:
            fn()
        except BadRequest as e:
            self.send_json(400, {"error": str(e)})
        except (BrokenPipeError, ConnectionResetError):
            pass
        except Exception as e:
            log("%s %s: %s" % (self.command, self.path, traceback.format_exc()))
            try:
                self.send_json(500, {"error": str(e) or type(e).__name__})
            except Exception:
                pass

    def route(self):
        u = urlparse(self.path)
        return u.path.rstrip("/") or "/", parse_qs(u.query)

    def file_path(self, path):
        name = unquote(path[len("/files/"):])
        if not FILE_NAME.match(name):
            raise BadRequest("bad file name %r" % name)
        return os.path.join(self.service.folder, name)

    def do_GET(self):
        self.run(self.get)

    def do_POST(self):
        self.run(self.post)

    def do_DELETE(self):
        self.run(self.delete)

    def get(self):
        path, _ = self.route()
        svc = self.service
        if path == "/info":
            self.send_json(200, {"model": MODEL, "sensor": SENSOR, "version": VERSION,
                                 "modes": [list(v) for v in RESOLUTIONS.values()]})
        elif path == "/settings":
            self.send_json(200, svc.settings())
        elif path == "/preview":
            data = svc.preview()
            self.send_response(200)
            self.send_header("Content-Type", "image/jpeg")
            self.send_header("Content-Length", str(len(data)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(data)
        elif path.startswith("/files/"):
            p = self.file_path(path)
            try:
                f = open(p, "rb")
            except FileNotFoundError:
                self.send_json(404, {"error": "no such file"})
                return
            with f:
                size = os.fstat(f.fileno()).st_size
                self.send_response(200)
                self.send_header("Content-Type", "image/jpeg" if p.endswith(".jpg") else "image/x-adobe-dng")
                self.send_header("Content-Length", str(size))
                self.end_headers()
                while True:
                    chunk = f.read(1 << 20)
                    if not chunk:
                        break
                    self.wfile.write(chunk)
        else:
            self.send_json(404, {"error": "not found"})

    def post(self):
        path, q = self.route()
        svc = self.service
        if path == "/shoot":
            self.body_json()  # drain any body
            self.send_response(200)
            self.send_header("Content-Type", "application/x-ndjson")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Transfer-Encoding", "chunked")
            self.end_headers()
            self.wfile.flush()

            def emit(obj):
                line = (json.dumps(obj) + "\n").encode()
                try:
                    self.wfile.write(b"%x\r\n%s\r\n" % (len(line), line))
                    self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError):
                    pass  # the photo is still saved; the client can't hear about it

            svc.shoot(emit)
            try:
                self.wfile.write(b"0\r\n\r\n")
                self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                pass
        elif path == "/settings":
            b = self.body_json()
            if not isinstance(b, dict):
                raise BadRequest("want {\"key\",\"value\"}")
            svc.set(b.get("key"), b.get("value"))
            self.send_empty(204)
        elif path == "/focus/auto":
            self.body_json()
            self.send_json(200, svc.focus_auto())
        elif path == "/focus/nudge":
            self.body_json()
            try:
                steps = int(q.get("steps", [""])[0])
            except ValueError:
                raise BadRequest("steps: want an integer")
            if steps == 0 or not -7 <= steps <= 7:
                raise BadRequest("steps: want -7 to 7, not 0")
            self.send_json(200, svc.nudge(steps))
        else:
            self.send_json(404, {"error": "not found"})

    def delete(self):
        path, _ = self.route()
        if path.startswith("/files/"):
            p = self.file_path(path)
            try:
                os.remove(p)
            except FileNotFoundError:
                self.send_json(404, {"error": "no such file"})
                return
            self.send_empty(204)
        else:
            self.send_json(404, {"error": "not found"})


def main():
    ap = argparse.ArgumentParser(description="The OwlSight camera over HTTP, for gosts-rig.")
    ap.add_argument("--port", type=int, default=8090)
    ap.add_argument("--host", default="", help="address to listen on (default: all)")
    ap.add_argument("--dir", default="/var/tmp/gosts-cam", help="scratch folder: photos and settings.json")
    ap.add_argument("--fake", action="store_true", help="no camera: a test pattern")
    a = ap.parse_args()

    os.makedirs(a.dir, exist_ok=True)
    cam = FakeCamera() if a.fake else PiCamera()
    svc = Service(cam, a.dir)
    Handler.service = svc
    srv = ThreadingHTTPServer((a.host, a.port), Handler)
    srv.daemon_threads = True
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
    log("gosts-cam %s: %s on :%d, files in %s" % (VERSION, "fake camera" if a.fake else MODEL, a.port, a.dir))
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        srv.server_close()
        svc.close()


if __name__ == "__main__":
    main()
