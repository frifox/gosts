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

## Download

Get `GoSTS-Rig-<version>-macos-arm64.zip` from the [Releases](https://github.com/frifox/gosts/releases) page (Apple
silicon Macs), unzip it and move **GoSTS Rig** to Applications. It's signed ad hoc, not by Apple, so the first time
macOS won't open it by double-click: try once, then System Settings → Privacy & Security → **Open Anyway** (on macOS
14 and earlier, right-click it → **Open** works too). For a real camera, also `brew install gphoto2`.

A release is made by pushing a version tag: GitHub Actions ([release.yml](../../.github/workflows/release.yml)) runs
the tests, builds the app (as `make` does) and attaches the zip to the tag's release.

```bash
git tag v0.1.0 && git push origin v0.1.0
```

## Build

Needs Go and the Xcode command line tools (`xcode-select --install`).

From the repository's root:

```bash
make           # builds cmd/gosts-rig-macos/build/GoSTS Rig.app
make install   # builds it, puts it in /Applications, and deletes the build
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
