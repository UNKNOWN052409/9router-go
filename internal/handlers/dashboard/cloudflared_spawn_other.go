//go:build !windows

package dashboard

import (
	"os"
	"os/exec"
	"syscall"
)

func isWindowsOS() bool { return false }

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

func cloudflaredSysProcAttr() *syscall.SysProcAttr { return nil }

func killCloudflaredTree(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
	_ = exec.Command("pkill", "-TERM", "-P", itoa(pid)).Run()
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
