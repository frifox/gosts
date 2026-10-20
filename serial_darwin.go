package gosts

import "golang.org/x/sys/unix"

// standardSpeeds are the speeds macOS' tcsetattr takes; others (the bus's
// 1 Mbps) are set another way (IOSSIOSPEED).
var standardSpeeds = map[uint64]bool{0: true, 50: true, 75: true, 110: true, 134: true, 150: true, 200: true, 300: true, 600: true, 1200: true,
	1800: true, 2400: true, 4800: true, 7200: true, 9600: true, 14400: true, 19200: true, 28800: true, 38400: true, 57600: true, 76800: true,
	115200: true, 230400: true}

// resetSpeed: device's speed back to a standard one, if it's kept a
// non-standard one (still open elsewhere: another program, or one that
// inherited it). The serial library reads the settings and writes them
// back as it opens a port, and macOS refuses a non-standard speed there
// ("error setting term settings: invalid argument").
func resetSpeed(device string) {
	fd, err := unix.Open(device, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	defer unix.Close(fd)
	t, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil || (standardSpeeds[t.Ispeed] && standardSpeeds[t.Ospeed]) {
		return
	}
	t.Ispeed, t.Ospeed = 9600, 9600
	unix.IoctlSetTermios(fd, unix.TIOCSETA, t)
}
