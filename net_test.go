package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNetEnrollmentProtectsAndRemovesEnvironmentKey(t *testing.T) {
	requireLinux(t)
	directory := t.TempDir()
	report := filepath.Join(directory, "report")
	installFakeNetBird(t, directory, `
key_file=""
previous=""
printf '%s\n' "$@" > "${NETBIRD_TEST_REPORT}.args"
for argument in "$@"; do
	if [ "$previous" = "--setup-key-file" ]; then key_file="$argument"; fi
	previous="$argument"
done
[ -n "$key_file" ] || exit 90
[ -z "${HI_NETBIRD_SETUP_KEY+x}" ] || exit 91
stat -c '%a' "$key_file" > "${NETBIRD_TEST_REPORT}.mode"
cat "$key_file" > "${NETBIRD_TEST_REPORT}.key"
printf '%s' "$key_file" > "${NETBIRD_TEST_REPORT}.path"
printf 'netbird failed safely\n' >&2
exit 23
`)

	secret := "setup-key-that-must-stay-secret"
	t.Setenv(netBirdSetupKeyEnv, secret)
	t.Setenv("NETBIRD_TEST_REPORT", report)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"net"}, strings.NewReader("\n"), &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("run returned %d, want 1; stderr: %s", exitCode, stderr.String())
	}

	arguments := readTestFile(t, report+".args")
	if strings.Contains(arguments, secret) {
		t.Fatal("setup key appeared in NetBird process arguments")
	}
	if !strings.Contains(arguments, "--setup-key-file\n") || !strings.Contains(arguments, "--hostname\n") {
		t.Fatalf("NetBird arguments did not include the setup-key file and hostname flags:\n%s", arguments)
	}
	if mode := strings.TrimSpace(readTestFile(t, report+".mode")); mode != "600" {
		t.Fatalf("temporary setup-key file mode = %s, want 600", mode)
	}
	if key := readTestFile(t, report+".key"); key != secret {
		t.Fatalf("temporary setup-key file contained %q, want the provided key", key)
	}
	keyPath := readTestFile(t, report+".path")
	if _, err := os.Stat(keyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary setup-key file still exists after NetBird failure: %v", err)
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
		t.Fatal("setup key appeared in command output")
	}
	if !strings.Contains(stderr.String(), "netbird failed safely") {
		t.Fatalf("NetBird stderr was not preserved: %s", stderr.String())
	}
}

func TestNetEnrollmentRejectsExposedKeyFileBeforeStartingNetBird(t *testing.T) {
	requireLinux(t)
	directory := t.TempDir()
	marker := filepath.Join(directory, "started")
	installFakeNetBird(t, directory, `
printf 'started' > "$NETBIRD_TEST_MARKER"
`)
	keyPath := filepath.Join(directory, "setup-key")
	if err := os.WriteFile(keyPath, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NETBIRD_TEST_MARKER", marker)

	var stderr bytes.Buffer
	exitCode := run([]string{"net", "--setup-key-file", keyPath}, strings.NewReader("\n"), &bytes.Buffer{}, &stderr)
	if exitCode != 1 {
		t.Fatalf("run returned %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "chmod 600") {
		t.Fatalf("error did not explain protected file permissions: %s", stderr.String())
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("NetBird started with an exposed key file: %v", err)
	}
}

func TestNetBirdReconnectUsesStoredEnrollment(t *testing.T) {
	requireLinux(t)
	directory := t.TempDir()
	logPath := filepath.Join(directory, "commands")
	installFakeNetBird(t, directory, `
printf '%s\n' "$1" >> "$NETBIRD_TEST_LOG"
printf 'netbird %s\n' "$1"
`)
	t.Setenv("NETBIRD_TEST_LOG", logPath)
	t.Setenv(netBirdSetupKeyEnv, "must-not-reach-netbird")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"net", "reconnect"}, strings.NewReader("unused"), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("run returned %d, want 0; stderr: %s", exitCode, stderr.String())
	}
	if commands := readTestFile(t, logPath); commands != "down\nup\n" {
		t.Fatalf("reconnect commands = %q, want down then up", commands)
	}
	if stdout.String() != "netbird down\nnetbird up\n" {
		t.Fatalf("NetBird output was not preserved: %q", stdout.String())
	}
}

func installFakeNetBird(t *testing.T, directory, body string) {
	t.Helper()
	path := filepath.Join(directory, "netbird")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("NetBird workstation commands support Linux")
	}
}
