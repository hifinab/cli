package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func connectNetBird(setupKey string, stdin io.Reader, stdout, stderr io.Writer) error {
	netbird, err := exec.LookPath("netbird")
	if err != nil {
		return errors.New("netbird is not installed; run `hi install` first")
	}
	currentHostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("read current hostname: %w", err)
	}
	deviceHostname, err := promptNetBirdHostname(stdin, stdout, currentHostname)
	if err != nil {
		return err
	}
	if err := runInteractive(stdin, stdout, stderr, netbird, "up", "--setup-key", setupKey, "--hostname", deviceHostname); err != nil {
		return fmt.Errorf("connect NetBird: %w", err)
	}
	return nil
}

func promptNetBirdHostname(r io.Reader, w io.Writer, current string) (string, error) {
	fmt.Fprintln(w, "NetBird will use this name as the device hostname shown to other peers.")
	fmt.Fprintf(w, "Device hostname [%s]: ", current)
	line, err := readLine(r)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read device hostname: %w", err)
	}

	hostname := strings.TrimSpace(line)
	if hostname == "" {
		return current, nil
	}
	if !validHostname(hostname) {
		return "", fmt.Errorf(
			"invalid device hostname %q; use dot-separated letters, digits, or hyphens (maximum 64 characters)",
			hostname,
		)
	}
	return hostname, nil
}
