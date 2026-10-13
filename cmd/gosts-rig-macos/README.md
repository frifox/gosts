# GoSTS Rig for macOS

[gosts-rig](../gosts-rig) as a regular Mac app: double-click it and the rig's page opens in its own window, no
Terminal or browser needed.

- Checks at launch what it needs, and says how to get anything missing: **gphoto2** for a real camera
  (`brew install gphoto2`), and macOS 14 on Apple silicon for **Build Model**. **Check Dependencies…** in the app
  menu checks again.
- Starts gosts-rig (bundled inside the app, with Build Model's tool) and stops it when you quit. If a capture or a
  model build is still going, it asks first.
- If gosts-rig is already running (from Terminal, say), it offers to use that one instead, since only one can drive
  the rig and the camera.
- Captures go to `~/Pictures/gosts-rig`, and settings are shared with the command-line gosts-rig.

## Build

Needs Go and the Xcode command line tools (`xcode-select --install`).

```bash
cmd/gosts-rig-macos/build.sh             # builds cmd/gosts-rig-macos/build/GoSTS Rig.app
cmd/gosts-rig-macos/build.sh --install   # and copies it to /Applications
```

The app is signed ad hoc, so it runs on the Mac that built it.

## Good to know

- **Help → Show Log** shows what gosts-rig printed (`~/Library/Logs/GoSTS Rig/`).
- **Help → Open in Browser** opens the same page in your browser.
- To try it on the simulators without touching your settings, start it from Terminal with extra gosts-rig flags:

  ```bash
  GOSTS_RIG_ARGS="--rig sim --camera sim --config /tmp/sim.toml --ctl-config /tmp/sim-ctl.toml" \
    "cmd/gosts-rig-macos/build/GoSTS Rig.app/Contents/MacOS/GoSTS Rig"
  ```
