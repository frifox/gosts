package main

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
)

// keepAwake keeps the Mac from sleeping (idle sleep, and system sleep on
// power) till the func it returns is called: a capture asleep halfway loses
// the driver board off USB. caffeinate's assertion, held while it runs (-w:
// it ends with the app too). Closing the lid still sleeps it. The display
// may sleep. Elsewhere: nothing.
func keepAwake() func() {
	if runtime.GOOS != "darwin" {
		return func() {}
	}
	cmd := exec.Command("caffeinate", "-i", "-s", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return func() {}
	}
	return func() {
		cmd.Process.Kill()
		cmd.Wait()
	}
}
