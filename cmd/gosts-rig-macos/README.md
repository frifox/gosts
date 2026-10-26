# GoSTS Rig for macOS

[gosts-rig](../gosts-rig) as a regular Mac app: double-click it and the rig's page opens in its own window, no
Terminal or browser needed.

- Checks at launch what it needs, and says how to get anything missing: **gphoto2** for a real camera
  (`brew install gphoto2`), and macOS 14 on Apple silicon for **Build Model**. **Check Dependencies…** in the app
  menu checks again.
- Starts gosts-rig (bundled inside the app, with Build Model's tool) and stops it when you quit. If a capture or a
  model build is still going, it asks first.
- If its port (8081) is taken, it asks: **Connect to It** (a gosts-rig already running, from Terminal, say), **Start
  New** on the next free port (the window's title says which), or **Quit**. Only one gosts-rig can drive the rig and
  the camera at a time.
- Captures go to `~/Pictures/gosts-rig`; **Settings…** (⌘,) picks another folder, or moves where the settings
  are kept. The settings are shared with the command-line gosts-rig.

## Download

Get `GoSTSRig-<version>-arm64.zip` from the [Releases](https://github.com/frifox/gosts/releases) page (Apple
silicon Macs), unzip it and move **GoSTS Rig** to Applications. It's signed with a Developer ID and notarized by
Apple, so it opens with a double-click. For a real camera, also `brew install gphoto2`.

Built here with `make`, it's signed ad hoc instead: fine on the Mac it's built on.

A release is made by pushing a `gosts-rig-v…` tag (plain `v…` tags are the library's Go module versions): GitHub
Actions ([release.yml](../../.github/workflows/release.yml)) runs the tests, builds the app (as `make` does, signed
with the Developer ID: `SIGN_IDENTITY`, see build.sh), has Apple notarize it, and attaches the zip to a release
titled "GoSTS Rig v1.0.0". It needs the repository's secrets `MACOS_CERT_P12_BASE64`, `MACOS_CERT_PASSWORD`,
`NOTARY_KEY_P8_BASE64`, `NOTARY_KEY_ID` and `NOTARY_ISSUER_ID` (listed in release.yml). Tags aren't sent by a plain `git push`: push the tag itself.

```bash
git tag gosts-rig-v1.0.0 && git push origin gosts-rig-v1.0.0
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

- **Help → Show Log** shows what gosts-rig printed: a log each start in `~/Library/Logs/GoSTS Rig/`, named after
  when it started (`gosts-rig.2026-10-14_12-00-00.log`), the last 10 kept.
- **Help → Open in Browser** opens the same page in your browser.
- To try it on the simulators without touching your settings, start it from Terminal with extra gosts-rig flags:

  ```bash
  GOSTS_RIG_ARGS="--rig sim --camera sim --config /tmp/sim.toml --ctl-config /tmp/sim-ctl.toml" \
    "cmd/gosts-rig-macos/build/GoSTS Rig.app/Contents/MacOS/GoSTS Rig"
  ```
