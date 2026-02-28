package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServerCtrlCExitsWithSingleKeypress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is required for this integration test")
	}
	workDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	tempDir := t.TempDir()
	binPath := filepath.Join(tempDir, "spadeloader-test-bin")
	buildCmd := exec.Command("go", "build", "-o", binPath, ".")
	buildCmd.Dir = workDir
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build spadeloader binary: %v\n%s", err, string(out))
	}

	port := freeLocalPort(t)
	baseDir := filepath.Join(tempDir, "data")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatalf("mkdir base dir: %v", err)
	}
	logPath := filepath.Join(tempDir, "server.log")

	sessionName := fmt.Sprintf("spadeloader-ctrlc-%d", time.Now().UnixNano())
	defer killTmuxSession(sessionName)

	serverCmd := fmt.Sprintf(
		"env SPADELOADER_USE_FAKE_FLASHER=1 SPADELOADER_DISCOVERY_ENABLE=1 SPADELOADER_LISTEN_ADDR=:%d SPADELOADER_BASE_DIR=%s %s server >%s 2>&1",
		port,
		shQuote(baseDir),
		shQuote(binPath),
		shQuote(logPath),
	)
	startCmd := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "sh", "-lc", serverCmd)
	if out, err := startCmd.CombinedOutput(); err != nil {
		t.Fatalf("start tmux session: %v\n%s", err, string(out))
	}

	if err := waitForHealthz(port, 10*time.Second); err != nil {
		t.Fatalf("server did not become healthy: %v\nlog:\n%s\npane:\n%s", err, readFileOrEmpty(logPath), captureTmuxPane(sessionName))
	}

	// Send terminal Ctrl-C key to exercise Bubble Tea key handling in raw mode.
	sendCtrlC := exec.Command("tmux", "send-keys", "-t", sessionName+":0.0", "C-c")
	if out, err := sendCtrlC.CombinedOutput(); err != nil {
		t.Fatalf("send Ctrl-C key: %v\n%s", err, string(out))
	}

	if err := waitForTmuxCommandExit(sessionName, 6*time.Second); err != nil {
		t.Fatalf("server did not exit after one Ctrl-C: %v\nlog:\n%s\npane:\n%s", err, readFileOrEmpty(logPath), captureTmuxPane(sessionName))
	}
}

func waitForHealthz(port int, timeout time.Duration) error {
	client := &http.Client{Timeout: 400 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)

	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("status=%d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out waiting for %s", url)
	}
	return lastErr
}

func waitForTmuxCommandExit(session string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		exited, err := tmuxCommandExited(session)
		if err != nil {
			return err
		}
		if exited {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout after %s", timeout)
}

func tmuxCommandExited(session string) (bool, error) {
	if !tmuxHasSession(session) {
		return true, nil
	}

	out, err := exec.Command("tmux", "list-panes", "-t", session, "-F", "#{pane_dead}").Output()
	if err != nil {
		if !tmuxHasSession(session) {
			return true, nil
		}
		return false, err
	}
	lines := strings.Fields(string(out))
	if len(lines) == 0 {
		return false, fmt.Errorf("no panes reported for session %q", session)
	}
	for _, line := range lines {
		if line == "0" {
			return false, nil
		}
	}
	return true, nil
}

func killTmuxSession(session string) {
	if !tmuxHasSession(session) {
		return
	}
	_, _ = exec.Command("tmux", "kill-session", "-t", session).CombinedOutput()
}

func captureTmuxPane(session string) string {
	if !tmuxHasSession(session) {
		return ""
	}
	out, err := exec.Command("tmux", "capture-pane", "-pt", session+":0.0", "-S", "-120").CombinedOutput()
	if err != nil {
		return ""
	}
	return string(out)
}

func tmuxHasSession(session string) bool {
	return exec.Command("tmux", "has-session", "-t", session).Run() == nil
}

func readFileOrEmpty(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func shQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'"
}

func freeLocalPort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free port: %v", err)
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected addr type %T", ln.Addr())
	}
	return addr.Port
}
