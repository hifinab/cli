package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// After an update, a running `hi server run` still runs the old binary
// until it restarts. hi update finds such servers, and the systemd unit
// that runs them, and offers to restart them.

// These are replaced in tests.
var (
	updateProcRoot   = "/proc"
	updateIsTerminal = isTerminal
	updateRun        = func(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
		command := exec.Command(name, args...)
		command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
		return command.Run()
	}
)

// staleServer is a hi server running a binary that has since been
// replaced.
type staleServer struct {
	pid  int
	unit string // such as hi-server.service, or "" when systemd doesn't run it
	user bool   // a systemd --user unit
}

// staleServers lists hi servers started from path whose binary has been
// replaced since: Linux shows their executable as "<path> (deleted)".
func staleServers(path string) []staleServer {
	entries, err := os.ReadDir(updateProcRoot)
	if err != nil {
		return nil
	}
	var servers []staleServer
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		dir := filepath.Join(updateProcRoot, entry.Name())
		executable, err := os.Readlink(filepath.Join(dir, "exe"))
		if err != nil || executable != path+" (deleted)" {
			continue
		}
		command, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(command), "\x00"), "\x00")
		if len(args) < 3 || args[1] != "server" || args[2] != "run" {
			continue
		}
		server := staleServer{pid: pid}
		server.unit, server.user = systemdUnit(filepath.Join(dir, "cgroup"))
		servers = append(servers, server)
	}
	return servers
}

// systemdUnit reads the service a process belongs to from its cgroup, such
// as 0::/system.slice/hi-server.service.
func systemdUnit(cgroupPath string) (string, bool) {
	data, err := os.ReadFile(cgroupPath)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		segments := strings.Split(parts[2], "/")
		for i := len(segments) - 1; i >= 0; i-- {
			if strings.HasSuffix(segments[i], ".service") {
				if strings.HasPrefix(segments[i], "user@") {
					return "", false // the user's manager itself, not a unit in it
				}
				return segments[i], strings.Contains(parts[2], "/user@")
			}
		}
	}
	return "", false
}

// restartStaleServers offers to restart servers that run an old binary.
// mode is "ask", "yes", or "no".
func restartStaleServers(path, newVersion, mode string, stdin io.Reader, stdout, stderr io.Writer) error {
	for _, server := range staleServers(path) {
		if server.unit == "" {
			fmt.Fprintf(stdout, "A hi server (process %d) still runs the old version; restart it to run %s.\n", server.pid, newVersion)
			continue
		}
		command := []string{"systemctl", "restart", server.unit}
		switch {
		case server.user:
			command = []string{"systemctl", "--user", "restart", server.unit}
		case os.Geteuid() != 0:
			command = append([]string{"sudo"}, command...)
		}
		line := strings.Join(command, " ")
		if mode == "no" || (mode == "ask" && !updateIsTerminal(stdin)) {
			fmt.Fprintf(stdout, "%s still runs the old version. Restart it to run %s:\n  %s\n", server.unit, newVersion, line)
			continue
		}
		if mode == "ask" {
			fmt.Fprintf(stdout, "%s still runs the old version. Restart it now so it runs %s? It is down for about a second. [Y/n] ", server.unit, newVersion)
			answer, _ := readLine(stdin)
			if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "" && answer != "y" && answer != "yes" {
				fmt.Fprintf(stdout, "Not restarted. When you are ready:\n  %s\n", line)
				continue
			}
		}
		fmt.Fprintf(stdout, "$ %s\n", line)
		if err := updateRun(stdin, stdout, stderr, command[0], command[1:]...); err != nil {
			return fmt.Errorf("%s failed: %w", line, err)
		}
		check := []string{"is-active", "--quiet", server.unit}
		if server.user {
			check = append([]string{"--user"}, check...)
		}
		if err := updateRun(nil, io.Discard, io.Discard, "systemctl", check...); err != nil {
			return fmt.Errorf("%s did not come back up; check `journalctl -u %s`", server.unit, server.unit)
		}
		fmt.Fprintf(stdout, "%s runs %s.\n", server.unit, newVersion)
	}
	return nil
}
