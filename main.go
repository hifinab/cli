package main

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

var version = "dev"

//go:embed scripts/install.sh
var setupScript []byte

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stdout)
		return 0
	}

	switch args[0] {
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	case "version", "-v", "--version":
		fmt.Fprintf(stdout, "hi %s\n", version)
		return 0
	case "adduser":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: hi adduser <username>")
			return 2
		}
		if err := addUser(args[1], stdin, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "hi: %v\n", err)
			return 1
		}
		return 0
	case "install":
		strix := len(args) == 2 && args[1] == "strix"
		if len(args) > 2 || (len(args) == 2 && !strix) {
			fmt.Fprintln(stderr, "usage: hi install [strix]")
			return 2
		}
		if err := installWorkstation(stdin, stdout, stderr, strix); err != nil {
			fmt.Fprintf(stderr, "hi: %v\n", err)
			return 1
		}
		return 0
	case "verify":
		if len(args) != 2 || args[1] != "strix" {
			fmt.Fprintln(stderr, "usage: hi verify strix")
			return 2
		}
		if result := verifyStrix(stdout); result.failures > 0 {
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "hi: unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `hi prepares Hifin development machines and projects.

Usage:
  hi adduser <name>   Create a user with render and video access
  hi install          Install general workstation software
  hi install strix    Install software and Strix Halo hardware support
  hi verify strix     Check an installed Strix Halo workstation
  hi version          Print the installed version
  hi help             Show this help`)
}

func installWorkstation(stdin io.Reader, stdout, stderr io.Writer, strix bool) error {
	if runtime.GOOS != "linux" {
		return errors.New("the installer supports Linux only")
	}
	if os.Geteuid() == 0 {
		return errors.New("run this command as your regular user; it uses sudo when needed")
	}
	currentHostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("read current hostname: %w", err)
	}
	requestedHostname, err := promptHostname(stdin, stdout, currentHostname)
	if err != nil {
		return err
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		return errors.New("bash is required")
	}

	script, err := os.CreateTemp("", "hi-install-*.sh")
	if err != nil {
		return fmt.Errorf("create temporary setup script: %w", err)
	}
	path := script.Name()
	defer os.Remove(path)

	if err := script.Chmod(0o700); err != nil {
		script.Close()
		return fmt.Errorf("secure temporary setup script: %w", err)
	}
	if _, err := script.Write(setupScript); err != nil {
		script.Close()
		return fmt.Errorf("write temporary setup script: %w", err)
	}
	if err := script.Close(); err != nil {
		return fmt.Errorf("close temporary setup script: %w", err)
	}

	profile := "standard"
	if strix {
		profile = "strix"
	}
	cmd := exec.Command(bash, path, profile, requestedHostname)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("%s setup failed with exit code %d", profile, exitErr.ExitCode())
		}
		return fmt.Errorf("start %s setup: %w", profile, err)
	}
	if strix {
		result := verifyStrix(stdout)
		if result.failures > 0 {
			return fmt.Errorf("installation completed with %d failed report check(s)", result.failures)
		}
	}
	return nil
}

func promptHostname(r io.Reader, w io.Writer, current string) (string, error) {
	fmt.Fprintf(w, "Current hostname: %s\nNew hostname (press Enter to keep it): ", current)
	line, err := readLine(r)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read hostname: %w", err)
	}

	hostname := strings.TrimSpace(line)
	if hostname == "" || hostname == current {
		return "", nil
	}
	if !validHostname(hostname) {
		return "", fmt.Errorf(
			"invalid hostname %q; use dot-separated letters, digits, or hyphens (maximum 64 characters)",
			hostname,
		)
	}
	return hostname, nil
}

func readLine(r io.Reader) (string, error) {
	var line strings.Builder
	var next [1]byte
	for {
		n, err := r.Read(next[:])
		if n == 1 {
			switch next[0] {
			case '\n':
				return line.String(), nil
			case '\r':
			default:
				line.WriteByte(next[0])
			}
		}
		if err != nil {
			return line.String(), err
		}
	}
}

func validHostname(hostname string) bool {
	if len(hostname) == 0 || len(hostname) > 64 {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') &&
				(char < 'A' || char > 'Z') &&
				(char < '0' || char > '9') &&
				char != '-' {
				return false
			}
		}
	}
	return true
}
