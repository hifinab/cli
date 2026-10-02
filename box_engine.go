package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// boxEngine runs containers: rootless Podman, or Docker as the fallback,
// whose daemon runs as root.
type boxEngine struct {
	name string // podman or docker
	bin  string
}

// These are replaced in tests.
var (
	boxCommand = func(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
		command := exec.Command(name, args...)
		command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
		return command.Run()
	}
	boxLookPath = exec.LookPath
)

func detectBoxEngine() (boxEngine, string, error) {
	if path, err := boxLookPath("podman"); err == nil {
		return boxEngine{name: "podman", bin: path}, "", nil
	}
	if path, err := boxLookPath("docker"); err == nil {
		return boxEngine{name: "docker", bin: path},
			"Podman is not installed, so hi box uses Docker, whose daemon runs as root: an escape from the box would be root on this machine. Install Podman with hi install podman.", nil
	}
	return boxEngine{}, "", errors.New("hi box needs Podman; install it with hi install podman")
}

// output runs the engine and returns its standard output.
func (e boxEngine) output(args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	err := boxCommand(nil, &stdout, &stderr, e.bin, args...)
	if err != nil {
		return stdout.String(), fmt.Errorf("%s %s: %s", e.name, strings.Join(args[:min(2, len(args))], " "), qFirstLine(stderr.String(), err.Error()))
	}
	return stdout.String(), nil
}

func (e boxEngine) interactive(stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	return boxCommand(stdin, stdout, stderr, e.bin, args...)
}

func (e boxEngine) exists(kind, name string) bool {
	_, err := e.output(kind, "inspect", name)
	return err == nil
}

// state is a container's state, such as running or exited, or "" when it
// doesn't exist.
func (e boxEngine) state(name string) string {
	out, err := e.output("container", "inspect", "--format", "{{.State.Status}}", name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (e boxEngine) defaultNetwork() string {
	if e.name == "docker" {
		return "bridge"
	}
	return "podman"
}

// userArgs run the box as the user, so files it writes are theirs.
func (e boxEngine) userArgs() []string {
	if e.name == "podman" {
		return []string{"--userns=keep-id"}
	}
	return []string{"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())}
}

// gpuArgs pass the AMD GPU in, with the render and video groups that open
// it. Podman needs crun for keep-groups.
func (e boxEngine) gpuArgs() []string {
	args := []string{"--device", "/dev/kfd", "--device", "/dev/dri"}
	if e.name == "podman" {
		return append(args, "--group-add", "keep-groups", "--security-opt", "label=disable")
	}
	for _, name := range []string{"render", "video"} {
		if group, err := user.LookupGroup(name); err == nil {
			args = append(args, "--group-add", group.Gid)
		}
	}
	return args
}

// boxHardening applies to every box: no capabilities, no privilege gain,
// and limits on processes and memory.
func boxHardening(memory string) []string {
	return []string{"--cap-drop=ALL", "--security-opt", "no-new-privileges", "--pids-limit", "4096", "--memory", memory}
}

// ---------------------------------------------------------------------------
// the base image

// boxContainerfile is hi's base image: Ubuntu with the tools agents use.
// The agents themselves come from the host, mounted read-only, so the box
// always runs the versions the user has.
const boxContainerfile = `FROM docker.io/library/ubuntu:24.04
RUN userdel -r ubuntu 2>/dev/null || true \
 && useradd --uid 1000 --home-dir /box/home --no-create-home --shell /bin/bash box \
 && apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    ca-certificates curl git less nano jq ripgrep fd-find tree unzip xz-utils zip \
    make build-essential pkg-config python3 python3-venv nodejs npm tmux procps file \
 && rm -rf /var/lib/apt/lists/* \
 && ln -s /usr/bin/fdfind /usr/local/bin/fd
RUN curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin UV_NO_MODIFY_PATH=1 sh
ENV HOME=/box/home LANG=C.UTF-8 TERM=xterm-256color
WORKDIR /box
`

func boxBaseImage() string {
	digest := sha256.Sum256([]byte(boxContainerfile))
	return "localhost/hi-box:" + hex.EncodeToString(digest[:])[:12]
}

// ensureImage builds hi's base image the first time, or a project's own
// Dockerfile, and returns the image to run.
func (e boxEngine) ensureBaseImage(stdout, stderr io.Writer) (string, error) {
	image := boxBaseImage()
	if e.exists("image", image) {
		return image, nil
	}
	fmt.Fprintf(stdout, "Building the hi box image %s (once, a few minutes)...\n", image)
	dir, err := os.MkdirTemp("", "hi-box-image-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte(boxContainerfile), 0o644); err != nil {
		return "", err
	}
	if err := e.interactive(nil, stdout, stderr, "build", "-t", image, "-f", filepath.Join(dir, "Containerfile"), dir); err != nil {
		return "", fmt.Errorf("building the box image failed: %w", err)
	}
	return image, nil
}

// ensureProjectImage builds a project's Dockerfile from devcontainer.json,
// tagged by the Dockerfile's content.
func (e boxEngine) ensureProjectImage(dockerfile, context string, stdout, stderr io.Writer) (string, error) {
	data, err := os.ReadFile(dockerfile)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append(data, []byte(context)...))
	image := "localhost/hi-box-project:" + hex.EncodeToString(digest[:])[:12]
	if e.exists("image", image) {
		return image, nil
	}
	fmt.Fprintf(stdout, "Building the project's image from %s...\n", dockerfile)
	if err := e.interactive(nil, stdout, stderr, "build", "-t", image, "-f", dockerfile, context); err != nil {
		return "", fmt.Errorf("building %s failed: %w", dockerfile, err)
	}
	return image, nil
}
