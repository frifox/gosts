#!/bin/sh
# Installs gosts-cam on a Raspberry Pi 5 (Raspberry Pi OS Trixie, 64-bit).
# Run from this folder on the Pi: sudo ./install.sh   (safe to run again)
set -eu

if [ "$(id -u)" -ne 0 ]; then
	echo "run with sudo: sudo $0" >&2
	exit 1
fi
here=$(cd "$(dirname "$0")" && pwd)

echo "== packages"
apt-get update
apt-get install -y python3-picamera2 rpi-usb-gadget

echo "== USB gadget mode (the Mac sees a USB Ethernet adapter)"
rpi-usb-gadget on

echo "== camera overlay"
cfg=/boot/firmware/config.txt
cp -n "$cfg" "$cfg.gosts-cam.bak" || true
# An auto-detecting camera_auto_detect=1 would load the wrong driver: comment it out.
sed -i 's/^[[:space:]]*camera_auto_detect=1/#&/' "$cfg"
grep -q '^\[all\]' "$cfg" || printf '\n[all]\n' >>"$cfg"
add() { # add $1 to the [all] section unless the file has it
	grep -qxF "$1" "$cfg" && return
	awk -v line="$1" '
		{ print }
		/^\[all\]/ && !done { print line; done = 1 }
	' "$cfg" >"$cfg.tmp" && mv "$cfg.tmp" "$cfg"
}
# Added in reverse: each goes right under [all].
add 'dtoverlay=ov64a40,link-frequency=456000000'
add 'camera_auto_detect=0'

echo "== /opt/gosts-cam"
install -d /opt/gosts-cam
install -m 0755 "$here/gosts_cam.py" /opt/gosts-cam/gosts_cam.py
install -m 0644 "$here/gosts-cam.service" /etc/systemd/system/gosts-cam.service
systemctl daemon-reload
systemctl enable gosts-cam.service
systemctl restart gosts-cam.service || true # no camera until the reboot: it retries

echo "== hostname"
hostnamectl set-hostname gosts-cam
grep -q 'gosts-cam' /etc/hosts || sed -i 's/^127\.0\.1\.1.*/127.0.1.1\tgosts-cam/' /etc/hosts

echo
echo "Done. Reboot to load the camera overlay and USB gadget mode: sudo reboot"
echo "Then from the Mac: curl 10.12.194.1:8090/info"
