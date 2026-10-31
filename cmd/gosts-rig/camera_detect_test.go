package main

import (
	"os"
	"os/exec"
	"slices"
	"testing"
)

// gphoto2 listing the A6600 twice (seen: usb:001,006, and usb:000,006 where
// macOS has a USB-C display cable): only the port the system has it at is
// kept; a model listed once, kept as it is.
func TestRealOnes(t *testing.T) {
	devs := []gphoto2Device{{"Sony Alpha-A6600 (PC Control)", "usb:001,006"}, {"Sony Alpha-A6600 (PC Control)", "usb:000,006"}, {"Canon EOS R5", "usb:002,003"}}
	vendors := func() map[[2]int]string { return map[[2]int]string{{1, 6}: "Sony", {0, 6}: "", {2, 3}: "Canon Inc."} }
	got := realOnes(devs, vendors)
	if len(got) != 2 || got[0].port != "usb:001,006" || got[1].model != "Canon EOS R5" {
		t.Errorf("got %v, want the A6600 at usb:001,006 and the Canon", got)
	}
	if got := realOnes(devs, func() map[[2]int]string { return nil }); len(got) != 3 {
		t.Errorf("devices unknown: %v, want all", got)
	}
	if got := realOnes(devs[:2], func() map[[2]int]string { return map[[2]int]string{} }); len(got) != 2 {
		t.Errorf("none confirmed: %v, want both kept", got)
	}
}

// parseIoreg on this machine's USB devices (macOS only), and the cameras
// gphoto2 sees now, with the phantoms dropped: printed.
func TestIoregLive(t *testing.T) {
	if os.Getenv("GOSTS_USB") == "" {
		t.Skip("GOSTS_USB=1: this machine's USB devices")
	}
	out, err := exec.Command("ioreg", "-p", "IOUSB", "-l", "-w0").Output()
	if err != nil {
		t.Skip(err)
	}
	v := parseIoreg(string(out))
	keys := slices.Collect(func(yield func([2]int) bool) {
		for k := range v {
			if !yield(k) {
				return
			}
		}
	})
	t.Logf("%d devices; %v", len(v), keys)
	t.Logf("cameras: %v", gphoto2Detect())
}
