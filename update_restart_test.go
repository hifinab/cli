package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProc makes a /proc with one process per entry.
func fakeProc(t *testing.T, processes map[string][3]string) {
	t.Helper()
	root := t.TempDir()
	for pid, process := range processes {
		dir := filepath.Join(root, pid)
		os.MkdirAll(dir, 0o755)
		if err := os.Symlink(process[0], filepath.Join(dir, "exe")); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "cmdline"), []byte(process[1]), 0o644)
		os.WriteFile(filepath.Join(dir, "cgroup"), []byte(process[2]), 0o644)
	}
	previous := updateProcRoot
	updateProcRoot = root
	t.Cleanup(func() { updateProcRoot = previous })
}

func recordUpdateRuns(t *testing.T) *[]string {
	t.Helper()
	var runs []string
	previous := updateRun
	updateRun = func(_ io.Reader, _, _ io.Writer, name string, args ...string) error {
		runs = append(runs, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	t.Cleanup(func() { updateRun = previous })
	return &runs
}

func TestUpdateRestartsStaleServers(t *testing.T) {
	requireLinux(t)
	if os.Geteuid() == 0 {
		t.Skip("as root, systemctl runs without sudo")
	}
	fakeReleases(t, "v0.9.0", false)
	path := installedBinary(t, "v0.7.0")
	fakeProc(t, map[string][3]string{
		"101": {path + " (deleted)", path + "\x00server\x00run\x00", "0::/system.slice/hi-server.service\n"},
		"102": {path + " (deleted)", path + "\x00server\x00run\x00", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/hi-test.service\n"},
		"103": {path + " (deleted)", path + "\x00server\x00run\x00--dir\x00x\x00", "0::/user.slice/user-1000.slice/session-3.scope\n"},
		"104": {path, path + "\x00server\x00run\x00", "0::/system.slice/fresh.service\n"},
		"105": {path + " (deleted)", path + "\x00q\x00", "0::/system.slice/other.service\n"},
	})
	runs := recordUpdateRuns(t)

	code, stdout, stderr := runUpdateTest("--restart")
	if code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	want := []string{
		"sudo systemctl restart hi-server.service", "systemctl is-active --quiet hi-server.service",
		"systemctl --user restart hi-test.service", "systemctl --user is-active --quiet hi-test.service",
	}
	if strings.Join(*runs, "|") != strings.Join(want, "|") {
		t.Fatalf("runs = %q", *runs)
	}
	for _, line := range []string{"hi-server.service runs v0.9.0.", "A hi server (process 103) still runs the old version"} {
		if !strings.Contains(stdout, line) {
			t.Errorf("output lacks %q:\n%s", line, stdout)
		}
	}
}

func TestUpdateAsksBeforeRestarting(t *testing.T) {
	requireLinux(t)
	installedBinary(t, "v0.9.0")
	fakeReleases(t, "v0.9.0", false)
	path, _ := updateExecutable()
	fakeProc(t, map[string][3]string{
		"101": {path + " (deleted)", path + "\x00server\x00run\x00", "0::/system.slice/hi-server.service\n"},
	})
	runs := recordUpdateRuns(t)

	// Already up to date, but the server runs an older binary: without a
	// terminal it only prints the command.
	code, stdout, _ := runUpdateTest()
	if code != 0 || !strings.Contains(stdout, "is up to date") || !strings.Contains(stdout, "hi-server.service still runs the old version") || len(*runs) != 0 {
		t.Fatalf("code %d runs %q:\n%s", code, *runs, stdout)
	}

	previous := updateIsTerminal
	updateIsTerminal = func(io.Reader) bool { return true }
	defer func() { updateIsTerminal = previous }()
	var out, errOut bytes.Buffer
	run([]string{"update"}, strings.NewReader("n\n"), &out, &errOut)
	if !strings.Contains(out.String(), "Not restarted") || len(*runs) != 0 {
		t.Fatalf("declined: runs %q\n%s", *runs, out.String())
	}
	out.Reset()
	run([]string{"update"}, strings.NewReader("\n"), &out, &errOut)
	if len(*runs) != 2 || !strings.HasSuffix((*runs)[0], "systemctl restart hi-server.service") {
		t.Fatalf("accepted: runs %q\n%s", *runs, out.String())
	}
	// --no-restart and --check never restart.
	*runs = nil
	runUpdateTest("--no-restart")
	runUpdateTest("--check")
	if len(*runs) != 0 {
		t.Fatalf("runs %q", *runs)
	}
}
