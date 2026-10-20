package gosts

import (
	"bytes"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// pty: a pseudo-terminal's other end (its path), its master kept open.
func pty(t *testing.T) string {
	m, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Skip("no pty:", err)
	}
	t.Cleanup(func() { unix.Close(m) })
	if err := unix.IoctlSetInt(m, unix.TIOCPTYGRANT, 0); err != nil {
		t.Skip(err)
	}
	if err := unix.IoctlSetInt(m, unix.TIOCPTYUNLK, 0); err != nil {
		t.Skip(err)
	}
	name := make([]byte, 128)
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(m), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&name[0]))); e != 0 {
		t.Skip(e)
	}
	return string(name[:bytes.IndexByte(name, 0)])
}

// The port Open opens isn't handed down to programs started afterwards (a
// camera's gphoto2 holding a copy kept it open, its speed with it, and it
// couldn't be opened again). (A pty: a standard speed; one can't take the
// bus's 1 Mbps.)
func TestOpenNotInherited(t *testing.T) {
	dev := pty(t)
	b, err := Open(dev, 115200)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	child := exec.Command("sleep", "2")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	out, _ := exec.Command("lsof", "-a", "-p", strconv.Itoa(child.Process.Pid), dev).CombinedOutput()
	if strings.Contains(string(out), "sleep") {
		t.Fatalf("a program started after has the port:\n%s", out)
	}
	// (and this process does: lsof sees ports here)
	if out, _ := exec.Command("lsof", dev).CombinedOutput(); !strings.Contains(string(out), dev) {
		t.Skipf("lsof doesn't list the pty:\n%s", out)
	}
}

// resetSpeed leaves a port at a standard speed as it is.
func TestResetSpeedStandard(t *testing.T) {
	dev := pty(t)
	fd, err := unix.Open(dev, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	tt, _ := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	tt.Ispeed, tt.Ospeed = 57600, 57600
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, tt); err != nil {
		t.Skip(err)
	}
	resetSpeed(dev)
	if got, _ := unix.IoctlGetTermios(fd, unix.TIOCGETA); got.Ispeed != 57600 {
		t.Errorf("speed %d, want 57600 kept", got.Ispeed)
	}
}
