package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const updateRepository = "hifinab/cli"

// These are replaced in tests.
var (
	updateAPIBase      = "https://api.github.com"
	updateDownloadBase = "https://github.com"
	updateExecutable   = os.Executable
)

var releaseTagPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

func runUpdate(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hi update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	requested := flags.String("version", "", "install this release, such as v0.7.0")
	check := flags.Bool("check", false, "only report whether an update is available")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: hi update [--version vX.Y.Z] [--check]")
		return 2
	}
	return exitCode(update(*requested, *check, stdout), stderr)
}

func update(requested string, check bool, stdout io.Writer) error {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return fmt.Errorf("releases exist for linux/amd64 and linux/arm64, not %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if requested != "" && !releaseTagPattern.MatchString(requested) {
		return usageError{fmt.Sprintf("invalid version %q; use a release tag such as v0.7.0", requested)}
	}

	target := requested
	if target == "" {
		latest, err := latestRelease()
		if err != nil {
			return err
		}
		target = latest
	}
	if target == version {
		fmt.Fprintf(stdout, "hi %s is up to date.\n", version)
		return nil
	}
	if check {
		fmt.Fprintf(stdout, "hi %s is available (installed: %s). Run `hi update` to install it.\n", target, version)
		return nil
	}

	path, err := updatablePath()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Updating hi %s to %s...\n", version, target)
	if err := replaceBinary(path, target); err != nil {
		return err
	}

	// Report what the new binary says it is, not what we asked for.
	output, err := exec.Command(path, "version").Output()
	if err != nil {
		return fmt.Errorf("installed %s at %s, but it did not run: %w", target, path, err)
	}
	fmt.Fprintf(stdout, "Updated %s to %s at %s.\n", version, strings.TrimPrefix(strings.TrimSpace(string(output)), "hi "), path)
	if home, err := os.UserHomeDir(); err == nil {
		if _, err := os.Stat(filepath.Join(home, skillDirectory, "SKILL.md")); err == nil {
			fmt.Fprintln(stdout, "Refresh your agent skill with `hi skill --global`, and `hi skill` in projects that have one.")
		}
	}
	return nil
}

func latestRelease() (string, error) {
	var release struct {
		TagName string `json:"tag_name"`
	}
	url := fmt.Sprintf("%s/repos/%s/releases/latest", updateAPIBase, updateRepository)
	data, err := httpGet(url, 30*time.Second, 1<<20)
	if err != nil {
		return "", fmt.Errorf("find the latest release: %w", err)
	}
	if err := json.Unmarshal(data, &release); err != nil || !releaseTagPattern.MatchString(release.TagName) {
		return "", fmt.Errorf("find the latest release: unexpected answer from GitHub")
	}
	return release.TagName, nil
}

// updatablePath is the running binary, if hi may replace it: a regular file
// owned by the current user in a directory the user can write to.
func updatablePath() (string, error) {
	executable, err := updateExecutable()
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file; reinstall with the installer", path)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		return "", fmt.Errorf("%s belongs to another user; update it as that user", path)
	}
	return path, nil
}

// replaceBinary downloads a release next to path, verifies its checksum, and
// renames it over path. Any failure leaves path untouched.
func replaceBinary(path, tag string) error {
	asset := "hi-linux-" + runtime.GOARCH
	base := fmt.Sprintf("%s/%s/releases/download/%s/", updateDownloadBase, updateRepository, tag)

	sums, err := httpGet(base+"checksums.txt", time.Minute, 1<<20)
	if err != nil {
		return fmt.Errorf("download checksums for %s: %w", tag, err)
	}
	expected := checksumFor(sums, asset)
	if expected == "" {
		return fmt.Errorf("the %s checksums do not list %s", tag, asset)
	}

	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".hi-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w", directory, err)
	}
	defer os.Remove(temporary.Name())

	if err := download(base+asset, temporary); err != nil {
		temporary.Close()
		return fmt.Errorf("download %s %s: %w", tag, asset, err)
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		temporary.Close()
		return err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, temporary); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != expected {
		return fmt.Errorf("checksum mismatch for %s %s; nothing was changed", tag, asset)
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := os.Chmod(temporary.Name(), info.Mode().Perm()|0o100); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}

func checksumFor(sums []byte, asset string) string {
	scanner := bufio.NewScanner(bytes.NewReader(sums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset && len(fields[0]) == 64 {
			return strings.ToLower(fields[0])
		}
	}
	return ""
}

func httpGet(url string, timeout time.Duration, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "hi/"+version)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusNotFound {
			return nil, errors.New("not found (does that release exist?)")
		}
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, limit))
}

func download(url string, destination io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "hi/"+version)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	_, err = io.Copy(destination, io.LimitReader(response.Body, 200<<20))
	return err
}
