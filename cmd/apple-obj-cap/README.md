# apple-obj-cap

Builds a 3D model from a folder of photos with Apple's Object Capture (RealityKit's `PhotogrammetrySession`), for
gosts-rig's Build Model. Every option the API has is a flag (`apple-obj-cap --help`); it reports what it does as JSON,
one event a line on stdout, so a program can follow it.

```sh
swiftc -O -parse-as-library -swift-version 5 main.swift -o apple-obj-cap   # macOS 14+, Xcode's command line tools
./apple-obj-cap ~/Pictures/gosts-rig/Benchy --detail preview
```

The photos (the folder's JPEG, PNG and HEIC files, by name) are fed one by one with their EXIF, and gravity from the
rig's `gravity.json` if there's one, numbered from 0, so each event names its photo. The model goes to
`<folder>/model` (it must be empty), the rest to `<folder>/model-build`: `poses.json` (each photo's camera pose),
`bounds.json`, `pointcloud.ply` and the checkpoint. Ctrl-C cancels; exit status 0 done, 1 failed, 2 cancelled.

Events: `start` (the files), `loaded`, `invalid` (with a reason), `skipped`, `progress` (fraction, stage, time
left), `aligned` (the photos placed), `downsampling`, `stitchingIncomplete`, `result`, `error`, `cancelled`, `done`.
