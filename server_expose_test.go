package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newExposedDataServer is a data server whose netbird expose is a fake
// that prints the URL of an httptest server standing in for NetBird's
// reverse proxy in front of the instance listener.
func newExposedDataServer(t *testing.T) (*testServer, *fakeHub, string) {
	t.Helper()
	ts, hub := newDataServer(t)
	public := httptest.NewServer(ts.server.instanceHandler())
	t.Cleanup(public.Close)
	script := filepath.Join(t.TempDir(), "netbird")
	writeTestFile(t, script, "#!/bin/sh\necho \"Service exposed successfully!\"\necho \"  URL:      "+public.URL+"\"\nexec sleep 600\n", 0o755)
	previous := exposeCommand
	exposeCommand = script
	t.Cleanup(func() { exposeCommand = previous; ts.server.stopExposure() })
	ts.server.exposure.listen = "127.0.0.1:7374"
	return ts, hub, public.URL
}

func TestRunTokensWorkOnlyThroughTheExposure(t *testing.T) {
	ts, hub, public := newExposedDataServer(t)
	client, _, err := dataClient()
	if err != nil {
		t.Fatal(err)
	}
	var access apiRunAccess
	if err := client.call(http.MethodPost, "/v1/data/run-access", map[string]any{"scopes": []string{"model:hifinab/ranker"}, "seconds": 3600, "run": "job1"}, &access); err != nil {
		t.Fatal(err)
	}
	if access.Endpoint != public+"/hf" || !strings.HasPrefix(access.Token, dataTokenPrefix) || access.Expires.Sub(computeNow()) < time.Hour {
		t.Fatalf("access: %+v", access)
	}
	if err := client.call(http.MethodPost, "/v1/data/run-access", map[string]any{"scopes": []string{"*"}}, &access); err == nil {
		t.Fatal("a run token for everything")
	}

	call := func(base, path, token string) *http.Response {
		request, _ := http.NewRequest(http.MethodHead, base+path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response
	}
	// Through the public URL, for its repository; links point back at it.
	response := call(public, "/hf/hifinab/ranker/resolve/main/config.json", access.Token)
	if response.StatusCode != http.StatusTemporaryRedirect || !strings.HasPrefix(response.Header.Get("Location"), public+"/hf/api/resolve-cache/") {
		t.Fatalf("through the exposure: %d %q", response.StatusCode, response.Header.Get("Location"))
	}
	if response := call(public, "/hf/api/models/hifinab/bars", access.Token); response.StatusCode != http.StatusForbidden {
		t.Fatalf("another repository: %d", response.StatusCode)
	}
	// A run token is no use inside NetBird, and a device's token none outside.
	if response := call(ts.url, "/hf/api/models/hifinab/ranker", access.Token); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a run token on the client listener: %d", response.StatusCode)
	}
	device := ts.aliceDevice(t)
	laptop := ts.server.issueDataToken(dataClaims{Device: device.Fingerprint, User: "alice", Scope: "*", Expires: computeNow().Add(time.Hour).Unix()})
	if response := call(public, "/hf/api/models/hifinab/ranker", laptop); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a device token through the exposure: %d", response.StatusCode)
	}
	// Only /hf and /health answer.
	for _, path := range []string{"/v1/me", "/v1/data", "/admin/users", "/"} {
		if response := call(public, path, access.Token); response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s through the exposure: %d", path, response.StatusCode)
		}
	}
	_ = hub
	audit, _ := os.ReadFile(filepath.Join(ts.dir, "audit.jsonl"))
	if !strings.Contains(string(audit), `"action":"exposure started"`) || !strings.Contains(string(audit), `"action":"run token"`) {
		t.Fatalf("audit = %s", audit)
	}
}

func TestComputeDataUsesTheExposureOnHuggingFace(t *testing.T) {
	_, _, public := newExposedDataServer(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "train.py")
	writeTestFile(t, script, "print('hi')\n", 0o644)
	request := runRequest{script: script, max: time.Hour, name: "job1"}
	var stderr strings.Builder
	cleanup, err := prepareDataRun(&request, []string{"model:hifinab/ranker"}, "hf", &stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer os.Unsetenv(dataRunTokenSecret)
	wrapper, _ := os.ReadFile(request.script)
	if !strings.Contains(string(wrapper), "HI_DATA_ENDPOINT") || strings.Contains(string(wrapper), dataTokenPrefix) ||
		!containsString(request.secrets, dataRunTokenSecret) || !strings.HasPrefix(os.Getenv(dataRunTokenSecret), dataTokenPrefix) ||
		!containsString(request.env, "HI_DATA_ENDPOINT="+public+"/hf") {
		t.Fatalf("request %+v\n%s\n%s", request, stderr.String(), wrapper)
	}
	// Colab keeps the signed links.
	request = runRequest{script: script}
	cleanup2, err := prepareDataRun(&request, []string{"dataset:hifinab/bars/data"}, "colab", &stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	if wrapper, _ := os.ReadFile(request.script); !strings.Contains(string(wrapper), "_PACKED") {
		t.Fatal("Colab didn't get signed links")
	}
}

func TestComputeDataFallsBackWithoutExposure(t *testing.T) {
	newDataServer(t) // exposure off: no instance listener
	dir := t.TempDir()
	script := filepath.Join(dir, "train.py")
	writeTestFile(t, script, "print('hi')\n", 0o644)
	request := runRequest{script: script}
	var stderr strings.Builder
	cleanup, err := prepareDataRun(&request, []string{"dataset:hifinab/bars/data"}, "hf", &stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	wrapper, _ := os.ReadFile(request.script)
	if !strings.Contains(stderr.String(), "using signed links instead") || !strings.Contains(string(wrapper), "_PACKED") || len(request.secrets) != 0 {
		t.Fatalf("fallback: %s", stderr.String())
	}
}

// TestDataRunProxyWrapper runs the proxy-mode wrapper with Python against
// a fake proxy and CDN: the token reaches the proxy, never the CDN.
func TestDataRunProxyWrapper(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	var cdnAuth []string
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnAuth = append(cdnAuth, r.Header.Get("Authorization"))
		w.Write([]byte(strings.Repeat("p", 4000)))
	}))
	defer cdn.Close()
	var proxyAuth []string
	var proxy *httptest.Server
	proxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyAuth = append(proxyAuth, r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/hf/api/datasets/o/bars/tree/abc" && r.URL.Query().Get("cursor") == "":
			w.Header().Set("Link", `<`+proxy.URL+`/hf/api/datasets/o/bars/tree/abc?recursive=true&cursor=2>; rel="next"`)
			w.Write([]byte(`[{"type":"file","path":"data/a.parquet","size":4000},{"type":"directory","path":"data"}]`))
		case r.URL.Path == "/hf/api/datasets/o/bars/tree/abc":
			w.Write([]byte(`[{"type":"file","path":"README.md","size":5},{"type":"file","path":"old/b.parquet","size":4000}]`))
		case r.URL.Path == "/hf/datasets/o/bars/resolve/abc/data/a.parquet":
			http.Redirect(w, r, cdn.URL+"/signed/a", http.StatusFound)
		case r.URL.Path == "/hf/datasets/o/bars/resolve/abc/README.md":
			http.Redirect(w, r, "/hf/api/resolve-cache/datasets/o/bars/abc/README.md", http.StatusTemporaryRedirect)
		case r.URL.Path == "/hf/api/resolve-cache/datasets/o/bars/abc/README.md":
			w.Write([]byte("hello"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer proxy.Close()

	script := "import os, sys\nprint('script', sys.argv[1:], sorted(os.listdir('data/bars')), os.environ['HF_ENDPOINT'].endswith('/hf'), os.environ['HF_TOKEN'], 'HI_DATA_TOKEN' in os.environ)\n"
	repos := []dataRunProxyRepo{{Kind: "dataset", ID: "o/bars", Revision: "abc", To: "data/bars", Include: "[dR]*"}}
	wrapper, err := dataRunProxyWrapper("train.py", []byte(script), repos)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "train.py"), string(wrapper), 0o644)
	command := exec.Command(python, "train.py", "--n", "1")
	command.Dir = dir
	command.Env = append(os.Environ(), "HI_DATA_ENDPOINT="+proxy.URL+"/hf", "HI_DATA_TOKEN=run-token")
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "script ['--n', '1'] ['README.md', 'data'] True run-token False") {
		t.Fatalf("wrapper: %v\n%s", err, output)
	}
	for _, auth := range proxyAuth {
		if auth != "Bearer run-token" {
			t.Fatalf("the proxy saw %q", proxyAuth)
		}
	}
	if len(cdnAuth) != 1 || cdnAuth[0] != "" {
		t.Fatalf("the CDN saw %q", cdnAuth)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "data/bars/README.md")); string(data) != "hello" {
		t.Fatalf("README = %q", data)
	}
}

func TestRevokeARunToken(t *testing.T) {
	ts, _, public := newExposedDataServer(t)
	socket := filepath.Join(ts.dir, "admin.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("no Unix socket here: %v", err)
	}
	admin := &http.Server{Handler: ts.server.adminHandler()}
	go admin.Serve(listener)
	defer admin.Close()

	client, _, err := dataClient()
	if err != nil {
		t.Fatal(err)
	}
	var first, second apiRunAccess
	for run, access := range map[string]*apiRunAccess{"job1": &first, "job2": &second} {
		if err := client.call(http.MethodPost, "/v1/data/run-access", map[string]any{"scopes": []string{"model:hifinab/ranker"}, "seconds": 3600, "run": run}, access); err != nil {
			t.Fatal(err)
		}
	}
	status := func(token string) int {
		request, _ := http.NewRequest(http.MethodHead, public+"/hf/hifinab/ranker/resolve/main/config.json", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if status(first.Token) != http.StatusTemporaryRedirect {
		t.Fatal("the run token doesn't work before revoking")
	}
	code, stdout, stderr := runHi("server", "expose", "--dir", ts.dir)
	if code != 0 || !strings.Contains(stdout, "job1") || !strings.Contains(stdout, "job2") || !strings.Contains(stdout, "model:hifinab/ranker") {
		t.Fatalf("expose: %d %s %s", code, stdout, stderr)
	}
	code, stdout, stderr = runHi("server", "expose", "revoke", "job1", "--dir", ts.dir)
	if code != 0 || !strings.Contains(stdout, "Revoked the run token of job1") {
		t.Fatalf("revoke: %d %s %s", code, stdout, stderr)
	}
	// Only job1's token stops working, and job1 can't get a new one.
	if status(first.Token) != http.StatusUnauthorized || status(second.Token) != http.StatusTemporaryRedirect {
		t.Fatal("revoking job1 changed the wrong tokens")
	}
	var again apiRunAccess
	if err := client.call(http.MethodPost, "/v1/data/run-access", map[string]any{"scopes": []string{"model:hifinab/ranker"}, "run": "job1"}, &again); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("a new token for a revoked run: %v", err)
	}
	if _, stdout, _ := runHi("server", "expose", "--dir", ts.dir); !strings.Contains(stdout, "revoked") {
		t.Fatalf("list after revoke: %s", stdout)
	}
	if code, _, stderr := runHi("server", "expose", "revoke", "nope", "--dir", ts.dir); code == 0 || !strings.Contains(stderr, "no run nope") {
		t.Fatalf("unknown run: %d %s", code, stderr)
	}
	// The revocation survives a restart, and is forgotten once the token
	// would have expired.
	reopened, err := openServer(ts.dir, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.runRevoked("job1") {
		t.Fatal("the revocation was lost on restart")
	}
	later := time.Now().Add(2 * time.Hour)
	previous := computeNow
	computeNow = func() time.Time { return later }
	defer func() { computeNow = previous }()
	ts.server.pruneRuns()
	if ts.server.runRevoked("job1") || len(ts.server.runList()) != 0 {
		t.Fatal("expired runs are kept")
	}
	audit, _ := os.ReadFile(filepath.Join(ts.dir, "audit.jsonl"))
	if !strings.Contains(string(audit), `"action":"run token revoked"`) {
		t.Fatalf("audit = %s", audit)
	}
}
