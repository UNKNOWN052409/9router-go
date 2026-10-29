package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/proc"
)

// isolate points DATA_DIR at a temp directory so the tests never read or write
// the operator's real pid file.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	return dir
}

func TestChildArgsDropsBackgroundFlag(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "drops long flag so the child never re-spawns itself",
			in:   []string{"--background", "--rtk=false"},
			want: []string{"--rtk=false"},
		},
		{
			name: "drops short alias",
			in:   []string{"-d", "--caveman"},
			want: []string{"--caveman"},
		},
		{
			name: "drops explicit true in both spellings",
			in:   []string{"--background=true", "-d=true"},
			want: []string{},
		},
		{
			name: "drops explicit false in both spellings",
			in:   []string{"--background=false", "-d=false", "--rtk"},
			want: []string{"--rtk"},
		},
		{
			name: "keeps unrelated flags in order",
			in:   []string{"--rtk=false", "-d", "--auto-update=true"},
			want: []string{"--rtk=false", "--auto-update=true"},
		},
		{
			name: "empty stays empty",
			in:   nil,
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := childArgs(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("childArgs(%v) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("childArgs(%v) = %v, want %v", tt.in, got, tt.want)
				}
			}
		})
	}
}

// The whole point of the pid file: a stale entry must read as "not running"
// and be cleaned up, never as a live daemon that blocks every later start.
func TestRunningPIDIgnoresStaleFile(t *testing.T) {
	isolate(t)
	writePIDFile(t, strconv.Itoa(deadPID(t)))

	if pid := RunningPID(); pid != 0 {
		t.Fatalf("RunningPID() = %d for a dead process, want 0", pid)
	}
	if _, err := os.Stat(PIDFile()); !os.IsNotExist(err) {
		t.Fatalf("stale pid file must be removed, stat err = %v", err)
	}
}

func TestRunningPIDMissingFile(t *testing.T) {
	isolate(t)
	if pid := RunningPID(); pid != 0 {
		t.Fatalf("RunningPID() = %d with no pid file, want 0", pid)
	}
}

func TestRunningPIDMalformedFile(t *testing.T) {
	isolate(t)
	writePIDFile(t, "not-a-pid\n")
	if pid := RunningPID(); pid != 0 {
		t.Fatalf("RunningPID() = %d for a malformed file, want 0", pid)
	}
}

// A live PID is what makes "already running" work: the second start must be
// refused instead of binding the same port twice. The claim has to name this
// binary as well as the number — a bare PID that later belongs to an unrelated
// process is not a daemon (issue #74).
func TestRunningPIDReportsLiveProcess(t *testing.T) {
	isolate(t)
	pid := livePID(t)
	writePIDFile(t, pidLine(pid, selfExecutable(t)))

	if got := RunningPID(); got != pid {
		t.Fatalf("RunningPID() = %d, want the live pid %d", got, pid)
	}
}

// UnregisterPID must not delete a claim made by a replacement process: the
// updater's new instance writes the pid before the old one exits.
func TestUnregisterPIDKeepsForeignClaim(t *testing.T) {
	isolate(t)

	RegisterPID()
	foreign := deadPID(t)
	writePIDFile(t, strconv.Itoa(foreign))

	UnregisterPID()

	data, err := os.ReadFile(PIDFile())
	if err != nil {
		t.Fatalf("pid file removed although another process owns it: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != strconv.Itoa(foreign) {
		t.Fatalf("pid file = %s, want the replacement's %d", got, foreign)
	}
}

func TestRegisterPIDReplacesStaleFile(t *testing.T) {
	isolate(t)
	writePIDFile(t, strconv.Itoa(deadPID(t)))

	RegisterPID()

	if pid := RunningPID(); pid != os.Getpid() {
		t.Fatalf("RunningPID() = %d after RegisterPID, want this process %d", pid, os.Getpid())
	}
}

func TestStopWithoutDaemonIsNoop(t *testing.T) {
	isolate(t)
	res, err := Stop()
	if err != nil {
		t.Fatalf("Stop() with no daemon = %v, want nil", err)
	}
	if res.Stopped {
		t.Fatal("Stop() must report not running when no pid file exists")
	}
	if res.Reason != "not_running" {
		t.Fatalf("Stop() reason = %q, want not_running", res.Reason)
	}
}

func TestLogTailReturnsLastLines(t *testing.T) {
	dir := isolate(t)

	logFile := filepath.Join(dir, "run", "gateway.log")
	if err := os.MkdirAll(filepath.Dir(logFile), 0755); err != nil {
		t.Fatalf("create log dir: %v", err)
	}
	if err := os.WriteFile(logFile, []byte("one\ntwo\nthree\nfour\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	if got, want := LogTail(2), "three\nfour"; got != want {
		t.Fatalf("LogTail(2) = %q, want %q", got, want)
	}
	if got, want := LogTail(99), "one\ntwo\nthree\nfour"; got != want {
		t.Fatalf("LogTail(99) = %q, want %q", got, want)
	}
	if got := LogTail(0); got != "one\ntwo\nthree\nfour" {
		t.Fatalf("LogTail(0) = %q, want the whole log", got)
	}
}

func TestLogTailMissingFile(t *testing.T) {
	isolate(t)
	if got := LogTail(10); got != "" {
		t.Fatalf("LogTail with no log file = %q, want empty", got)
	}
}

// writePIDFile puts content in the pid file, creating the run directory.
func writePIDFile(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(Dir(), 0755); err != nil {
		t.Fatalf("create run dir: %v", err)
	}
	if err := os.WriteFile(PIDFile(), []byte(content), 0644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
}

// livePID starts a process that stays alive for the duration of the test, so
// liveness checks have a real positive to answer.
func livePID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestDaemonLivePIDHelper")
	cmd.Env = append(os.Environ(), "9ROUTER_PID_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd.Process.Pid
}

// TestDaemonLivePIDHelper is the payload livePID spawns; it only sleeps when
// the environment marker says it is the helper rather than a real test run.
func TestDaemonLivePIDHelper(t *testing.T) {
	if os.Getenv("9ROUTER_PID_HELPER") != "1" {
		t.Skip("helper payload, only meaningful as a spawned child")
	}
	time.Sleep(30 * time.Second)
}

// deadPID returns a PID that is not running, so liveness has a real negative.
func deadPID(t *testing.T) int {
	t.Helper()
	for pid := 4_000_000; pid < 4_002_000; pid++ {
		if !proc.Alive(pid) {
			return pid
		}
	}
	t.Skip("no unused PID found in the probe range")
	return 0
}

// A port that answers with anything that is not a 9router-go /api/version
// JSON must not count as busy: documented deployments share the port with a
// coexisting service (a proxy on 127.0.0.1 while the gateway binds the
// wildcard), and the wildcard bind decides whether we can serve.
func TestPortBusyIgnoresForeignListeners(t *testing.T) {
	// A non-gateway responder (HTML like a foreign dashboard, or a bare
	// TCP echo) must read as not busy.
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><html><body>not us</body></html>"))
	}))
	defer html.Close()
	if busy, who := portBusy(html.URL); busy {
		t.Fatalf("HTML responder counted as busy: %q", who)
	}

	// A responder that serves the updater's /api/version JSON IS a
	// 9router-go and must read as busy even without a pid-file claim.
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"currentVersion":"1.9.16","goVersion":"go1.27.3"}`))
	}))
	defer gateway.Close()
	busy, who := portBusy(gateway.URL)
	if !busy {
		t.Fatalf("gateway JSON responder not counted as busy")
	}
	if who != "started outside this binary" {
		t.Fatalf("unexpected busy reason: %q", who)
	}

	// A listener that closes without answering HTTP (plain accept, no
	// response) is not a gateway either.
	// The Node 9router's /api/version (reachable through a port-sharing
	// proxy) lacks goVersion — it must not count as this gateway either.
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"currentVersion":"0.5.104","latestVersion":"0.5.112","hasUpdate":true}`))
	}))
	defer node.Close()
	if busy, _ := portBusy(node.URL); busy {
		t.Fatalf("node-router version JSON counted as busy")
	}

	// A listener that closes without answering HTTP (plain accept, no
	// response) is not a gateway either.
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _ := w.(http.Hijacker).Hijack()
		if conn != nil {
			_ = conn.Close()
		}
	}))
	defer closed.Close()
	if busy, _ := portBusy(closed.URL); busy {
		t.Fatalf("non-HTTP responder counted as busy")
	}
}
