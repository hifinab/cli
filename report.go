package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

type reportResult struct {
	failures int
	pending  int
}

type reportStatus string

const (
	reportPass    reportStatus = "PASS"
	reportFail    reportStatus = "FAIL"
	reportPending reportStatus = "PENDING"
)

type reportCheck struct {
	status reportStatus
	label  string
}

func verifyStrix(w io.Writer) reportResult {
	needsReboot := pathExists("/run/reboot-required")
	configuredGroups := userHasGroups(true, "render", "video")
	activeGroups := userHasGroups(false, "render", "video")

	checks := []reportCheck{
		check("Ubuntu 26.04 amd64", ubuntu2604AMD64()),
		check("ROCm gfx1151 package installed", packageInstalled("amdrocm10.0-gfx1151")),
		check("User configured for render and video groups", configuredGroups),
		check("Docker package installed", packageInstalled("docker-ce")),
		check("Docker service active", serviceActive("docker.service")),
		check("NetBird service active", serviceActive("netbird.service")),
		check("GitHub CLI package installed", packageInstalled("gh")),
		check("amd-debug-tools installed with pipx", pipxPackageInstalled("amd-debug-tools")),
		check("Docker Compose available", runSuccessful("docker", "compose", "version")),
	}

	for _, tool := range []string{
		"gh", "docker", "uv", "pipx", "btop", "tmux", "node", "npm", "omp", "claude", "codex", "herdr", "netbird", "amd-smi", "rocminfo",
	} {
		checks = append(checks, check("Command available: "+tool, toolAvailable(tool)))
	}

	if activeGroups {
		checks = append(checks, reportCheck{status: reportPass, label: "Current session has render and video access"})
	} else if configuredGroups {
		checks = append(checks, reportCheck{status: reportPending, label: "Current session needs a logout or reboot for GPU group access"})
	} else {
		checks = append(checks, reportCheck{status: reportFail, label: "Current session has render and video access"})
	}

	devicesReady := pathExists("/dev/kfd") && globExists("/dev/dri/renderD*")
	checks = append(checks, runtimeCheck("GPU device nodes available", devicesReady, needsReboot))

	rocmReady := runOutputContains("rocminfo", "gfx1151") && runOutputContains("amd-smi", "GPU:", "list")
	checks = append(checks, runtimeCheck("ROCm detects the gfx1151 GPU", rocmReady, needsReboot || !activeGroups))

	fmt.Fprintln(w, "\n==> Strix installation report")
	result := reportResult{}
	for _, item := range checks {
		fmt.Fprintf(w, "[%s] %s\n", item.status, item.label)
		switch item.status {
		case reportFail:
			result.failures++
		case reportPending:
			result.pending++
		}
	}

	fmt.Fprintf(w, "\nResult: %d passed, %d failed, %d pending.\n", len(checks)-result.failures-result.pending, result.failures, result.pending)
	switch {
	case result.failures > 0:
		fmt.Fprintln(w, "Action: resolve failed checks, then run `hi verify strix` again.")
	case needsReboot:
		fmt.Fprintln(w, "Action: reboot, then run `hi verify strix` for the final hardware check.")
	case result.pending > 0:
		fmt.Fprintln(w, "Action: log out and back in, then run `hi verify strix` again.")
	default:
		fmt.Fprintln(w, "The Strix workstation is ready.")
	}
	return result
}

func check(label string, ok bool) reportCheck {
	status := reportFail
	if ok {
		status = reportPass
	}
	return reportCheck{status: status, label: label}
}

func runtimeCheck(label string, ok, pending bool) reportCheck {
	if ok {
		return reportCheck{status: reportPass, label: label}
	}
	if pending {
		return reportCheck{status: reportPending, label: label + " after reboot"}
	}
	return reportCheck{status: reportFail, label: label}
}

func ubuntu2604AMD64() bool {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return false
	}
	contents, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return false
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(contents), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			values[key] = strings.Trim(value, `"`)
		}
	}
	return values["ID"] == "ubuntu" && values["VERSION_ID"] == "26.04"
}

func packageInstalled(name string) bool {
	command := exec.Command("dpkg-query", "-W", "-f=${db:Status-Status}", name)
	output, err := command.Output()
	return err == nil && strings.TrimSpace(string(output)) == "installed"
}

func pipxPackageInstalled(name string) bool {
	command := exec.Command("pipx", "list", "--json")
	output, err := command.Output()
	return err == nil && strings.Contains(string(output), `"`+name+`"`)
}

func serviceActive(name string) bool {
	return runSuccessful("systemctl", "is-active", "--quiet", name)
}

func runSuccessful(name string, args ...string) bool {
	command := exec.Command(name, args...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run() == nil
}

func runOutputContains(name, expected string, args ...string) bool {
	output, err := exec.Command(name, args...).CombinedOutput()
	return err == nil && strings.Contains(string(output), expected)
}

func toolAvailable(name string) bool {
	if _, err := exec.LookPath(name); err == nil {
		return true
	}
	current, err := user.Current()
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(current.HomeDir, ".local", "bin", name))
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func userHasGroups(configured bool, required ...string) bool {
	args := []string{"-nG"}
	if configured {
		current, err := user.Current()
		if err != nil {
			return false
		}
		args = append(args, current.Username)
	}
	output, err := exec.Command("id", args...).Output()
	if err != nil {
		return false
	}
	groups := make(map[string]bool)
	for _, group := range strings.Fields(string(output)) {
		groups[group] = true
	}
	for _, group := range required {
		if !groups[group] {
			return false
		}
	}
	return true
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func globExists(pattern string) bool {
	matches, err := filepath.Glob(pattern)
	return err == nil && len(matches) > 0
}
