package daemon

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// maxIdleRSS is PRD R30's budget for an idle daemon.
const maxIdleRSS = 50 << 20

// TestIdleRSSIsUnderBudget builds and runs the real openaird binary rather than
// measuring this test process, because a Go test binary carries the testing
// package, the race detector's bookkeeping when enabled, and every dependency
// any test in this package imports. Measuring that would answer a question
// nobody asked.
//
// It is skipped under -short: it compiles a binary and waits for it to settle.
func TestIdleRSSIsUnderBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the daemon binary")
	}
	if runtime.GOOS != "linux" {
		t.Skip("reads RSS from /proc")
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "openaird")
	build := exec.Command("go", "build", "-o", bin, "github.com/shreyashsri79/openair/cmd/openaird")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build openaird: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin,
		"--listen", "127.0.0.1:0",
		"--keys", filepath.Join(dir, "keys"),
		"--dir", filepath.Join(dir, "inbox"),
		"--socket", filepath.Join(dir, "d.sock"),
		"--no-announce",
		"--quiet",
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start openaird: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// Let it bind, allocate its buffers, and settle.
	waitForSocket(t, filepath.Join(dir, "d.sock"))
	time.Sleep(2 * time.Second)

	rss, err := readRSS(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("read RSS: %v", err)
	}
	t.Logf("idle RSS: %.1f MiB", float64(rss)/(1<<20))
	if rss > maxIdleRSS {
		t.Errorf("idle RSS %.1f MiB exceeds the %d MiB budget (PRD R30)",
			float64(rss)/(1<<20), maxIdleRSS>>20)
	}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon never created %s", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// readRSS reports resident set size in bytes, from VmRSS in /proc.
func readRSS(pid int) (uint64, error) {
	f, err := os.Open("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, err
		}
		return kb * 1024, nil
	}
	return 0, sc.Err()
}
