# GoSTS Rig for macOS (cmd/gosts-rig-macos).
#
#   make           builds cmd/gosts-rig-macos/build/GoSTS Rig.app
#   make install   builds it, then puts it in /Applications

APP := cmd/gosts-rig-macos/build/GoSTS Rig.app

.PHONY: build install clean

build:
	cmd/gosts-rig-macos/build.sh

install: build
	rm -rf "/Applications/GoSTS Rig.app"
	ditto "$(APP)" "/Applications/GoSTS Rig.app"
	@echo "installed /Applications/GoSTS Rig.app"

clean:
	rm -rf cmd/gosts-rig-macos/build
