package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	colabInstallHint = "install with `uv tool install google-colab-cli`"
	colabListTimeout = time.Minute
	colabNewTimeout  = 10 * time.Minute
	colabStopTimeout = 2 * time.Minute
)

// colabProvider drives the Google Colab CLI (google-colab-cli). Colab has no
// public API, so this is the one provider that shells out to its CLI.
type colabProvider struct{}

// Rates are compute units per hour measured in colab-runner on 2026-09-27;
// `colab usage` reports the account's live balance and rate.
var colabHardware = []computeHardware{
	{name: "cpu", kind: "CPU", memory: "12 GB RAM", rate: "~0.08 units/h", paid: false},
	{name: "T4", kind: "GPU", memory: "15 GB VRAM", rate: "~1.07 units/h", paid: true},
	{name: "L4", kind: "GPU", memory: "24 GB VRAM", rate: "units/h unmeasured", paid: true},
	{name: "G4", kind: "GPU", memory: "96 GB VRAM", rate: "~8.9 units/h", paid: true},
	{name: "A100", kind: "GPU", memory: "40 GB VRAM", rate: "units/h unmeasured", paid: true},
	{name: "H100", kind: "GPU", memory: "80 GB VRAM", rate: "units/h unmeasured", paid: true},
	{name: "v5e1", kind: "TPU", memory: "16 GB HBM", rate: "units/h unmeasured", paid: true},
	{name: "v6e1", kind: "TPU", memory: "32 GB HBM", rate: "units/h unmeasured", paid: true},
}

// A session line looks like
// `[name] endpoint | Hardware: T4 | Shape: Standard | Variant: GPU`.
// Orphaned server-side sessions use `?` as the name.
var colabSessionLine = regexp.MustCompile(
	`^\[([^\]]+)\] (\S+) \| Hardware: ([^|]+?) \| Shape: ([^|]+?) \| Variant: (\S+)`)

func (colabProvider) name() string { return "colab" }

func (colabProvider) maxLifetime() time.Duration { return 24 * time.Hour }

func (colabProvider) reservedPorts() map[int]string {
	return map[int]string{8080: "used by Colab's own proxy"}
}

func (colabProvider) hardware() ([]computeHardware, error) { return colabHardware, nil }

// Colab sessions cannot stop themselves, so hi watches the deadline locally.
func (colabProvider) enforcesLifetime() bool { return false }

func (p colabProvider) validateRun(request runRequest) error {
	switch {
	case request.script == "":
		return usageError{"colab runs Python scripts only; container images are not supported"}
	case len(request.secrets) > 0:
		return usageError{"colab has no secret store; --secret would expose the value in process arguments"}
	case request.detach:
		return usageError{"colab runs cannot detach; use `hi compute up` and `hi compute ssh` for long work"}
	case request.namespace != "":
		return usageError{"--namespace applies to Hugging Face only"}
	}
	return nil
}

func (p colabProvider) logs(name string, follow bool, lines int, stdin io.Reader, stdout, stderr io.Writer) error {
	return sshLogs(p, name, follow, lines, stdin, stdout, stderr)
}

func (colabProvider) wait(name string, stdout, stderr io.Writer) (int, error) {
	return 0, errors.New("colab runs finish in the foreground; there is nothing to wait for")
}

func colabBinary() (string, error) {
	if path, err := exec.LookPath("colab"); err == nil {
		return path, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		path := filepath.Join(home, ".local", "bin", "colab")
		if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o111 != 0 {
			return path, nil
		}
	}
	return "", errors.New("the Colab CLI is not installed; " + colabInstallHint)
}

func colabTokenPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "colab-cli", "token.json")
}

func (colabProvider) check() providerStatus {
	status := providerStatus{}
	if _, err := colabBinary(); err != nil {
		status.hints = append(status.hints, colabInstallHint)
		return status
	}
	status.installed = true
	if _, err := os.Stat(colabTokenPath()); err == nil {
		status.signedIn = true
	} else {
		status.hints = append(status.hints,
			"sign in once with `colab usage` (opens a browser; paste the code back)")
	}
	if !hasColabSSHKey() {
		status.hints = append(status.hints,
			"ssh and tunnels need a key: run `ssh-keygen -t ed25519`")
	}
	return status
}

func hasColabSSHKey() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, key := range []string{"id_ed25519", "id_ecdsa"} {
		if _, err := os.Stat(filepath.Join(home, ".ssh", key)); err == nil {
			return true
		}
	}
	return false
}

// colabOutput runs a short Colab CLI command with its own deadline, because
// the CLI can hang past its internal timeouts.
func colabOutput(timeout time.Duration, args ...string) (string, error) {
	binary, err := colabBinary()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var output bytes.Buffer
	command := exec.CommandContext(ctx, binary, args...)
	command.Stdout = &output
	command.Stderr = &output
	err = command.Run()
	if ctx.Err() != nil {
		return output.String(), fmt.Errorf("colab %s did not answer within %s", args[0], timeout)
	}
	if err != nil {
		return output.String(), fmt.Errorf("colab %s failed: %s", args[0], strings.TrimSpace(output.String()))
	}
	return output.String(), nil
}

func (colabProvider) list() ([]computeInstance, error) {
	output, err := colabOutput(colabListTimeout, "sessions")
	if err != nil {
		return nil, err
	}
	return parseColabSessions(output), nil
}

func parseColabSessions(output string) []computeInstance {
	var instances []computeInstance
	for _, line := range strings.Split(output, "\n") {
		match := colabSessionLine.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		hardware := strings.TrimSpace(match[3])
		if strings.EqualFold(hardware, "CPU") {
			hardware = "cpu"
		}
		instances = append(instances, computeInstance{
			name:     match[1],
			hardware: hardware,
			shape:    strings.TrimSpace(match[4]),
			detail:   match[2],
		})
	}
	return instances
}

func (colabProvider) account() (string, error) {
	if _, err := os.Stat(colabTokenPath()); err != nil {
		return "", nil
	}
	output, err := colabOutput(colabListTimeout, "usage")
	if err != nil {
		return "", err
	}
	return formatColabUsage(output), nil
}

func formatColabUsage(output string) string {
	var balance, rate string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(line, "Current balance: "); ok {
			balance = strings.TrimSuffix(value, " compute units")
		}
		if value, ok := strings.CutPrefix(line, "Usage rate: "); ok {
			rate = value
		}
	}
	if balance == "" {
		return ""
	}
	summary := "Colab balance: " + balance + " compute units"
	if rate != "" {
		summary += ", currently using " + strings.TrimSuffix(rate, "/hr") + " units/h"
	}
	return summary
}

func colabAcceleratorArgs(hardware computeHardware, highMem bool) []string {
	var args []string
	switch hardware.kind {
	case "GPU":
		args = append(args, "--gpu", hardware.name)
	case "TPU":
		args = append(args, "--tpu", hardware.name)
	}
	if highMem {
		args = append(args, "--high-mem")
	}
	return args
}

func (colabProvider) upCommand(request upRequest) []string {
	// --image and --namespace do not apply to Colab; startInstance rejects them.
	args := append([]string{"colab", "new", "-s", request.name},
		colabAcceleratorArgs(request.hardware, request.highMem)...)
	return args
}

func (p colabProvider) create(request upRequest, stdout, stderr io.Writer) error {
	binary, err := colabBinary()
	if err != nil {
		return err
	}
	args := p.upCommand(request)[1:]
	ctx, cancel := context.WithTimeout(context.Background(), colabNewTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("colab new did not finish within %s", colabNewTimeout)
		}
		return fmt.Errorf("colab could not start %s: %w", request.name, err)
	}

	// The CLI has silently substituted hardware before, so confirm what we got.
	instances, err := p.list()
	if err != nil {
		return err
	}
	for _, instance := range instances {
		if instance.name != request.name {
			continue
		}
		if !strings.EqualFold(instance.hardware, request.hardware.name) {
			fmt.Fprintf(stderr, "hi: warning: asked for %s but Colab started %s; stop it with `hi compute stop %s` if that is wrong\n",
				request.hardware.name, instance.hardware, request.name)
		}
		return nil
	}
	return fmt.Errorf("colab reported success but %s is not listed; check `colab sessions`", request.name)
}

func (p colabProvider) stop(name string, stdout, stderr io.Writer) error {
	output, err := colabOutput(colabStopTimeout, "stop", "-s", name)
	if err != nil {
		return err
	}
	// `colab stop` exits successfully even for unknown names.
	if strings.Contains(output, "not found") {
		return fmt.Errorf("colab has no session named %q", name)
	}
	fmt.Fprintf(stdout, "Stopped %s.\n", name)
	return nil
}

func (colabProvider) ssh(name string) (sshTarget, error) {
	binary, err := colabBinary()
	if err != nil {
		return sshTarget{}, err
	}
	if !hasColabSSHKey() {
		return sshTarget{}, errors.New("no SSH key found; run `ssh-keygen -t ed25519` first")
	}
	proxy := fmt.Sprintf("%s ssh --proxy-mode -s %s", shellQuote(binary), name)
	return sshTarget{
		options: []string{
			"-o", "ProxyCommand=" + proxy,
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
			"-o", "ServerAliveInterval=30",
		},
		destination: "root@colab",
	}, nil
}

func (colabProvider) runCommand(request runRequest) []string {
	args := []string{"colab", "run"}
	if request.name != "" {
		args = append(args, "-s", request.name)
	}
	args = append(args, colabAcceleratorArgs(request.hardware, request.highMem)...)
	args = append(args, "--timeout", strconv.Itoa(int(request.max.Seconds())))
	for _, entry := range request.env {
		args = append(args, "--env", entry)
	}
	args = append(args, request.script)
	return append(args, request.args...)
}

func (p colabProvider) runJob(request runRequest, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	binary, err := colabBinary()
	if err != nil {
		return 0, err
	}
	command := exec.Command(binary, p.runCommand(request)[1:]...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	err = command.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return 0, err
	}
	return 0, nil
}

func shellQuote(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+", r))
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
