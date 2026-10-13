#!/bin/sh
# build.sh: builds "GoSTS Rig.app" into cmd/gosts-rig-macos/build/: the app
# (main.swift), with gosts-rig and apple-obj-cap (Build Model's tool) inside
# it. Needs Go and the Xcode command line tools (xcode-select --install).
# make runs it (make install: and puts the app in /Applications).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
app="$here/build/GoSTS Rig.app"
version=$(git -C "$root" describe --tags --always --dirty 2>/dev/null || echo dev)

rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"

echo "gosts-rig"
(cd "$root" && go build -trimpath -o "$app/Contents/MacOS/gosts-rig" ./cmd/gosts-rig)

echo "apple-obj-cap"
swiftc -O -parse-as-library -swift-version 5 "$root/cmd/apple-obj-cap/main.swift" -o "$app/Contents/MacOS/apple-obj-cap"

echo "GoSTS Rig"
swiftc -O -swift-version 5 -framework AppKit -framework WebKit "$here/main.swift" -o "$app/Contents/MacOS/GoSTS Rig"

echo "icon"
tmp=$(mktemp -d)
swift "$here/icon.swift" "$here/icon.svg" "$tmp/AppIcon.iconset"
iconutil -c icns "$tmp/AppIcon.iconset" -o "$app/Contents/Resources/AppIcon.icns"
rm -rf "$tmp"

sed "s/VERSION/$version/g" "$here/Info.plist" > "$app/Contents/Info.plist"

# Signed ad hoc (not by a developer): fine on the Mac it's built on.
codesign --force --sign - "$app/Contents/MacOS/gosts-rig" "$app/Contents/MacOS/apple-obj-cap"
codesign --force --sign - "$app"

echo "built $app ($version)"
