//go:build unix

package gosts

import "golang.org/x/sys/unix"

// closeOnExec marks every open descriptor that isn't close-on-exec so. The
// serial library opens its port without it, so every program started
// afterwards (a camera's gphoto2, say) would get a copy and hold the port
// open: on macOS its settings (the bus's non-standard speed) then stay,
// and opening it again fails. (Go's own files have it already: only
// strays like the port's are changed.)
func closeOnExec() {
	n := uint64(4096)
	var r unix.Rlimit
	if unix.Getrlimit(unix.RLIMIT_NOFILE, &r) == nil && r.Cur < 1<<16 {
		n = max(n, r.Cur)
	}
	for fd := 3; uint64(fd) < n; fd++ {
		fl, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err == nil && fl&unix.FD_CLOEXEC == 0 {
			unix.FcntlInt(uintptr(fd), unix.F_SETFD, fl|unix.FD_CLOEXEC)
		}
	}
}
