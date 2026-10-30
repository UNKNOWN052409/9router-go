package dashboard

// Named-domain Cloudflare tunnels driven by the dashboard's Connect Domain
// flow. The operator pastes the connector token (Cloudflare Zero Trust →
// Tunnels → the tunnel's token, the long eyJ… string) — optionally with their
// own hostname for display — and the gateway runs `cloudflared tunnel run
// --token …` detached from the server process. The token persists in
// settings, so every gateway start brings the domain back up too
// (StartCloudflaredFromSettings, called from the server's ready hook).
//
// No credentials live in this file: the token is the operator's, read and
// written through the same settings store every other dashboard flag uses.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

var (
	cloudflaredMu    sync.Mutex
	cloudflaredCmd   *exec.Cmd
	cloudflaredBinFn = findCloudflaredBin
	// runningFn is the seam tests pin: a dashboard test must not depend on
	// whether this machine happens to have a connector up (the same shape as
	// tailscaleBinFn/tailscaleExec above).
	runningFn = func() bool {
		return cloudflaredCmd != nil && cloudflaredCmd.Process != nil
	}
)

// findCloudflaredBin locates the cloudflared executable: PATH first, then the
// data dir's bin folder, then the 9router config dir's bin (the Node router
// downloads its own copy there).
func findCloudflaredBin() string {
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p
	}
	suffix := ""
	if isWindowsOS() {
		suffix = ".exe"
	}
	for _, dir := range []string{os.Getenv("DATA_DIR"), filepath.Join(homeDir(), ".9router")} {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, "bin", "cloudflared"+suffix)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// CloudflaredRunning reports whether a tunnel connector is currently managed.
func CloudflaredRunning() bool {
	cloudflaredMu.Lock()
	defer cloudflaredMu.Unlock()
	return runningFn()
}

// StartCloudflared launches `cloudflared tunnel run --token <token>` detached.
// Starting twice is a no-op; a connector that exits on its own clears the
// handle so a later enable can start a fresh one.
func StartCloudflared(token string) error {
	cloudflaredMu.Lock()
	defer cloudflaredMu.Unlock()
	if cloudflaredCmd != nil && cloudflaredCmd.Process != nil {
		return nil
	}
	bin := cloudflaredBinFn()
	if bin == "" {
		return fmt.Errorf("cloudflared is not installed — download it from https://developers.cloudflare.com/cloudflare/one-panel/connectors/ or place it in %s", filepath.Join(homeDir(), ".9router", "bin"))
	}
	cmd := exec.Command(bin, "tunnel", "run", "--token", token)
	cmd.SysProcAttr = cloudflaredSysProcAttr()
	logPath := filepath.Join(cloudflaredLogDir(), "cloudflared.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		cmd.Stdout = f
		cmd.Stderr = f
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cloudflared start: %w", err)
	}
	cloudflaredCmd = cmd
	go func(c *exec.Cmd) {
		_ = c.Wait()
		cloudflaredMu.Lock()
		if cloudflaredCmd == c {
			cloudflaredCmd = nil
		}
		cloudflaredMu.Unlock()
	}(cmd)
	return nil
}

func cloudflaredLogDir() string {
	if d := os.Getenv("DATA_DIR"); d != "" {
		return filepath.Join(d, "run")
	}
	h := homeDir()
	if h == "" {
		return "."
	}
	return filepath.Join(h, ".9router", "run")
}

// StopCloudflared kills the managed connector, including its children.
func StopCloudflared() {
	cloudflaredMu.Lock()
	cmd := cloudflaredCmd
	cloudflaredCmd = nil
	cloudflaredMu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	killCloudflaredTree(cmd.Process.Pid)
}
