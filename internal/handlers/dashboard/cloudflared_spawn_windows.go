//go:build windows

package dashboard

import (
	"os"
	"os/exec"
	"syscall"
)

func isWindowsOS() bool { return true }

func homeDir() string { return os.Getenv("USERPROFILE") }

// Detached + hidden: the connector outlives a dashboard restart cycle and
// never flashes a console window.
func cloudflaredSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000008 /*DETACHED_PROCESS*/}
}

func killCloudflaredTree(pid int) {
	_ = exec.Command("taskkill", "/pid", itoa(pid), "/T", "/F").Run()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
