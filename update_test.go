package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeReleases serves GitHub's latest-release API and release assets. Each
// release's binary is a script that prints its version.
func fakeReleases(t *testing.T, latest string, tamper bool) string {
	return fakeReleasesWith(t, latest, tamper, true, nil)
}

// fakeReleasesWith can turn off the /releases/latest redirect and count API
// calls, to test which way the latest release is found.
func fakeReleasesWith(t *testing.T, latest string, tamper, redirect bool, apiCalls *int) string {
	t.Helper()
	asset := "hi-linux-" + runtime.GOARCH
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/hifinab/cli/releases/latest":
			if apiCalls != nil {
				*apiCalls++
			}
			fmt.Fprintf(w, `{"tag_name": %q}`, latest)
		case r.URL.Path == "/hifinab/cli/releases/latest" && redirect:
			http.Redirect(w, r, "/hifinab/cli/releases/tag/"+latest, http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/hifinab/cli/releases/download/"):
			parts := strings.Split(r.URL.Path, "/")
			tag, file := parts[5], parts[6]
			if tag == "v0.0.404" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			binary := fakeBinary(tag)
			switch file {
			case asset:
				if tamper {
					binary += "# tampered\n"
				}
				fmt.Fprint(w, binary)
			case "checksums.txt":
				sum := sha256.Sum256([]byte(binary))
				fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	previousAPI, previousDownload := updateAPIBase, updateDownloadBase
	updateAPIBase, updateDownloadBase = server.URL, server.URL
	t.Cleanup(func() { updateAPIBase, updateDownloadBase = previousAPI, previousDownload })
	return server.URL
}

func fakeBinary(tag string) string {
	return "#!/bin/sh\necho 'hi " + tag + "'\n"
}

// installedBinary makes a fake installed hi and points the updater at it.
func installedBinary(t *testing.T, installed string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hi")
	writeTestFile(t, path, fakeBinary(installed), 0o755)
	previous := updateExecutable
	updateExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { updateExecutable = previous })
	previousVersion := version
	version = installed
	t.Cleanup(func() { version = previousVersion })
	return path
}

func runUpdateTest(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"update"}, args...), strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestUpdateInstallsTheLatestReleaseAndReportsBothVersions(t *testing.T) {
	requireLinux(t)
	fakeReleases(t, "v0.9.0", false)
	path := installedBinary(t, "v0.7.0")

	code, stdout, stderr := runUpdateTest()
	if code != 0 {
		t.Fatalf("exit code = %d; stderr: %s", code, stderr)
	}
	if got := readTestFile(t, path); got != fakeBinary("v0.9.0") {
		t.Fatalf("the binary was not replaced:\n%s", got)
	}
	if !strings.Contains(stdout, "Updated v0.7.0 to v0.9.0 at "+path) {
		t.Fatalf("output did not report both versions and the path:\n%s", stdout)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("temporary files were left behind: %v", entries)
	}
}

func TestUpdateToTheInstalledVersionIsANoOp(t *testing.T) {
	requireLinux(t)
	fakeReleases(t, "v0.7.0", false)
	path := installedBinary(t, "v0.7.0")
	before, _ := os.Stat(path)

	code, stdout, _ := runUpdateTest()
	if code != 0 || !strings.Contains(stdout, "hi v0.7.0 is up to date.") {
		t.Fatalf("exit code = %d, stdout = %s", code, stdout)
	}
	if after, _ := os.Stat(path); !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("the binary was rewritten")
	}
}

func TestUpdateToAnExplicitVersion(t *testing.T) {
	requireLinux(t)
	fakeReleases(t, "v0.9.0", false)
	path := installedBinary(t, "v0.9.0")

	if code, _, stderr := runUpdateTest("--version", "v0.6.0"); code != 0 {
		t.Fatalf("exit code = %d; stderr: %s", code, stderr)
	}
	if got := readTestFile(t, path); got != fakeBinary("v0.6.0") {
		t.Fatalf("the requested version was not installed:\n%s", got)
	}
	if code, _, stderr := runUpdateTest("--version", "latest"); code != 2 || !strings.Contains(stderr, "invalid version") {
		t.Fatalf("invalid tag: exit code = %d, stderr = %s", code, stderr)
	}
	if code, _, stderr := runUpdateTest("--version", "v0.0.404"); code != 1 || !strings.Contains(stderr, "does that release exist") {
		t.Fatalf("missing release: exit code = %d, stderr = %s", code, stderr)
	}
	if got := readTestFile(t, path); got != fakeBinary("v0.6.0") {
		t.Fatal("a failed update changed the binary")
	}
}

func TestUpdateRefusesAChecksumMismatch(t *testing.T) {
	requireLinux(t)
	fakeReleases(t, "v0.9.0", true)
	path := installedBinary(t, "v0.7.0")

	code, _, stderr := runUpdateTest()
	if code != 1 || !strings.Contains(stderr, "checksum mismatch") {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if got := readTestFile(t, path); got != fakeBinary("v0.7.0") {
		t.Fatal("the binary was replaced despite the mismatch")
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("temporary files were left behind: %v", entries)
	}
}

func TestUpdateCheckChangesNothing(t *testing.T) {
	requireLinux(t)
	fakeReleases(t, "v0.9.0", false)
	path := installedBinary(t, "v0.7.0")

	code, stdout, _ := runUpdateTest("--check")
	if code != 0 || !strings.Contains(stdout, "hi v0.9.0 is available (installed: v0.7.0)") {
		t.Fatalf("exit code = %d, stdout = %s", code, stdout)
	}
	if got := readTestFile(t, path); got != fakeBinary("v0.7.0") {
		t.Fatal("--check replaced the binary")
	}
}

func TestUpdateRefusesAnUnwritableDirectory(t *testing.T) {
	requireLinux(t)
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	fakeReleases(t, "v0.9.0", false)
	path := installedBinary(t, "v0.7.0")
	directory := filepath.Dir(path)
	if err := os.Chmod(directory, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(directory, 0o755) })

	code, _, stderr := runUpdateTest()
	if code != 1 || !strings.Contains(stderr, "cannot write to") {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if got := readTestFile(t, path); got != fakeBinary("v0.7.0") {
		t.Fatal("the binary changed")
	}
}

func TestUpdateFindsTheLatestReleaseWithoutTheAPI(t *testing.T) {
	requireLinux(t)
	calls := 0
	fakeReleasesWith(t, "v0.9.0", false, true, &calls)
	installedBinary(t, "v0.7.0")
	if code, stdout, stderr := runUpdateTest("--check"); code != 0 || !strings.Contains(stdout, "v0.9.0 is available") {
		t.Fatalf("exit code = %d, stdout = %s, stderr = %s", code, stdout, stderr)
	}
	if calls != 0 {
		t.Fatalf("the rate-limited API was called %d times", calls)
	}
}

func TestUpdateFallsBackToTheAPI(t *testing.T) {
	requireLinux(t)
	calls := 0
	fakeReleasesWith(t, "v0.9.0", false, false, &calls)
	installedBinary(t, "v0.7.0")
	if code, stdout, stderr := runUpdateTest("--check"); code != 0 || !strings.Contains(stdout, "v0.9.0 is available") {
		t.Fatalf("exit code = %d, stdout = %s, stderr = %s", code, stdout, stderr)
	}
	if calls != 1 {
		t.Fatalf("API calls = %d, want the fallback to be used once", calls)
	}
}
