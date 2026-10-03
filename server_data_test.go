package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHub is a Hugging Face Hub that knows one organization, hifinab, and
// records the calls the proxy passes on.
type fakeHub struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  []string // "METHOD path auth"
}

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	hub := &fakeHub{}
	hub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		hub.mu.Lock()
		hub.calls = append(hub.calls, r.Method+" "+r.URL.EscapedPath()+" "+auth)
		hub.mu.Unlock()
		if auth != "Bearer hf_team" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"Invalid credentials"}`))
			return
		}
		updated := `"lastModified":"2026-09-30T12:00:00.000Z"`
		switch {
		case r.URL.Path == "/api/whoami-v2":
			w.Write([]byte(`{"name":"svc","orgs":[{"name":"hifinab"}],"auth":{"accessToken":{"role":"read"}}}`))
		case r.URL.Path == "/api/datasets" && r.URL.Query().Get("author") == "hifinab":
			w.Write([]byte(`[{"id":"hifinab/bars","private":true,` + updated + `,"sha":"abc123","mainSize":12400000000},` +
				`{"id":"hifinab/fills","private":true,` + updated + `,"sha":"def456","mainSize":3100000000}]`))
		case r.URL.Path == "/api/models" && r.URL.Query().Get("author") == "hifinab":
			w.Write([]byte(`[{"id":"hifinab/ranker","private":true,` + updated + `,"sha":"0a1b2c"},` +
				`{"id":"hifinab/bars","private":true,` + updated + `,"sha":"999999"}]`))
		case r.URL.Path == "/api/buckets/hifinab":
			w.Write([]byte(`[{"id":"hifinab/scratch","private":true,"updatedAt":"2026-09-30T12:00:00.000Z","size":80200000000,"totalFiles":412}]`))
		case r.URL.Path == "/api/buckets/hifinab/scratch/tree":
			w.Write([]byte(`[{"type":"directory","path":"runs"},{"type":"file","path":"runs/a.parquet","size":7000},{"type":"file","path":"notes.md","size":12}]`))
		case r.URL.Path == "/api/datasets" || r.URL.Path == "/api/models" || strings.HasPrefix(r.URL.Path, "/api/buckets/") && strings.Count(r.URL.Path, "/") == 3:
			w.Write([]byte(`[]`))
		case r.URL.Path == "/hifinab/ranker/resolve/main/config.json":
			w.Header().Set("Location", "/api/resolve-cache/models/hifinab/ranker/0a1b2c/config.json")
			w.Header().Set("Link", `<`+hub.server.URL+`/api/models/hifinab/ranker/xet-read-token/0a1b2c>; rel="xet-auth"`)
			w.WriteHeader(http.StatusTemporaryRedirect)
		case r.URL.Path == "/datasets/hifinab/bars/resolve/main/data/day.parquet":
			w.Header().Set("Location", "https://cdn.example/signed/day.parquet?Expires=1")
			w.Header().Set("X-Linked-Size", "5000")
			w.WriteHeader(http.StatusFound)
		default:
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	t.Cleanup(hub.server.Close)
	previous := dataHubURL
	dataHubURL = hub.server.URL
	t.Cleanup(func() { dataHubURL = previous })
	return hub
}

func (h *fakeHub) passed() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...)
}

// newDataServer is a test server serving hifinab, with alice's device
// enrolled in group students.
func newDataServer(t *testing.T) (*testServer, *fakeHub) {
	t.Helper()
	hub := newFakeHub(t)
	ts := newTestServer(t)
	if _, err := ts.server.addDataOrgs([]string{"hifinab"}, "hf_team", "bob"); err != nil {
		t.Fatal(err)
	}
	ts.connectAs(t, "alice", "students")
	return ts, hub
}

func (ts *testServer) aliceDevice(t *testing.T) serverDevice {
	t.Helper()
	ts.server.mu.Lock()
	defer ts.server.mu.Unlock()
	for _, device := range ts.server.state.Devices {
		if device.User == "alice" {
			return *device
		}
	}
	t.Fatal("alice has no device")
	return serverDevice{}
}

// proxyCall makes a call to the proxy as hf would.
func proxyCall(t *testing.T, ts *testServer, method, path, token string) *http.Response {
	t.Helper()
	request, _ := http.NewRequest(method, ts.url+path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return response
}

func TestAddingADataOrgChecksTheToken(t *testing.T) {
	newFakeHub(t)
	ts := newTestServer(t)
	if _, err := ts.server.addDataOrgs([]string{"hifinab"}, "hf_wrong", "bob"); err == nil || !strings.Contains(err.Error(), "refused the token") {
		t.Fatalf("a wrong token: %v", err)
	}
	if _, err := ts.server.addDataOrgs([]string{"hifinab", "elsewhere"}, "hf_team", "bob"); err == nil || !strings.Contains(err.Error(), "not a member of elsewhere") {
		t.Fatalf("an organization the account isn't in: %v", err)
	}
	if len(ts.server.dataOrgs()) != 0 {
		t.Fatal("a failed add stored a token")
	}
	results, err := ts.server.addDataOrgs([]string{"hifinab"}, "hf_team", "bob")
	if err != nil || len(results) != 1 || results[0].Datasets != 2 || results[0].Models != 2 || results[0].Buckets != 1 || results[0].Account != "svc" {
		t.Fatalf("add: %+v, %v", results, err)
	}
	keys, _ := os.ReadFile(filepath.Join(ts.dir, "keys.json"))
	if !strings.Contains(string(keys), `"data:hifinab": "hf_team"`) {
		t.Fatalf("keys.json = %s", keys)
	}
	audit, _ := os.ReadFile(filepath.Join(ts.dir, "audit.jsonl"))
	if !strings.Contains(string(audit), "connected data organization") || strings.Contains(string(audit), "hf_team") {
		t.Fatalf("audit = %s", audit)
	}
}

func TestHiDataListsWhatTheGroupMayRead(t *testing.T) {
	ts, _ := newDataServer(t)
	code, stdout, stderr := runHi("data", "ls")
	if code != 0 || !strings.Contains(stdout, "dataset  hifinab/bars") || !strings.Contains(stdout, "12.4 GB") ||
		!strings.Contains(stdout, "model    hifinab/ranker") || !strings.Contains(stdout, "bucket   hifinab/scratch") || !strings.Contains(stdout, "80.2 GB") {
		t.Fatalf("ls: code %d\n%s%s", code, stdout, stderr)
	}
	code, stdout, _ = runHi("data", "ls", "--kind", "model", "--json")
	var catalog apiDataCatalog
	if code != 0 || json.Unmarshal([]byte(stdout), &catalog) != nil || len(catalog.Items) != 2 || catalog.Items[0].Kind != "model" {
		t.Fatalf("ls --json: code %d\n%s", code, stdout)
	}

	writeTestFile(t, policyPath(ts.dir), `{"groups":{"students":{"data":["hifinab/fills"]}}}`, 0o600)
	code, stdout, _ = runHi("data", "ls")
	if code != 0 || strings.Contains(stdout, "hifinab/bars") || !strings.Contains(stdout, "hifinab/fills") {
		t.Fatalf("ls with a policy:\n%s", stdout)
	}
	writeTestFile(t, policyPath(ts.dir), `{"groups":{"students":{"data":[]}}}`, 0o600)
	if _, stdout, _ = runHi("data", "ls"); !strings.Contains(stdout, "Nothing for you to download") {
		t.Fatalf("ls with an empty list:\n%s", stdout)
	}
}

func TestDataTokensFollowPolicy(t *testing.T) {
	ts, _ := newDataServer(t)
	client, connection, err := dataClient()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := dataToken(client, connection, "dataset:hifinab/bars"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dataToken(client, connection, "dataset:other/bars"); err == nil || !strings.Contains(err.Error(), "doesn't serve the other organization") {
		t.Fatalf("another organization: %v", err)
	}
	if _, _, err := dataToken(client, connection, "space:hifinab/app"); err == nil || !strings.Contains(err.Error(), "invalid scope") {
		t.Fatalf("a space: %v", err)
	}
	writeTestFile(t, policyPath(ts.dir), `{"groups":{"students":{"data":["hifinab/fills"]}}}`, 0o600)
	if _, _, err := dataToken(client, connection, "dataset:hifinab/bars"); err == nil || !strings.Contains(err.Error(), "may not read hifinab/bars") {
		t.Fatalf("a repository outside policy: %v", err)
	}
}

func TestDataProxyPassesReadsWithTheTeamToken(t *testing.T) {
	ts, hub := newDataServer(t)
	device := ts.aliceDevice(t)
	token := ts.server.issueDataToken(dataClaims{Device: device.Fingerprint, User: "alice", Scope: "model:hifinab/ranker",
		Expires: computeNow().Add(time.Hour).Unix()})

	response := proxyCall(t, ts, http.MethodHead, "/hf/hifinab/ranker/resolve/main/config.json", token)
	if response.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("resolve: %d", response.StatusCode)
	}
	// Redirects and links within the Hub stay on the proxy.
	if location := response.Header.Get("Location"); location != ts.url+"/hf/api/resolve-cache/models/hifinab/ranker/0a1b2c/config.json" {
		t.Fatalf("Location = %q", location)
	}
	if link := response.Header.Get("Link"); !strings.Contains(link, ts.url+"/hf/api/models/hifinab/ranker/xet-read-token/0a1b2c") {
		t.Fatalf("Link = %q", link)
	}
	if response := proxyCall(t, ts, http.MethodGet, "/hf/api/resolve-cache/models/hifinab/ranker/0a1b2c/onnx%2Fconfig.json", token); response.StatusCode != http.StatusOK {
		t.Fatalf("resolve-cache with a nested file: %d", response.StatusCode)
	}
	for _, path := range []string{"/hf/api/models/hifinab/ranker/revision/main", "/hf/api/models/hifinab/ranker/tree/main?recursive=true",
		"/hf/api/models/hifinab/ranker/xet-read-token/0a1b2c"} {
		if response := proxyCall(t, ts, http.MethodGet, path, token); response.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", path, response.StatusCode)
		}
	}
	calls := hub.passed()
	last := calls[len(calls)-1]
	if !strings.HasSuffix(last, "Bearer hf_team") || strings.Contains(strings.Join(calls, "\n"), dataTokenPrefix) {
		t.Fatalf("the hub saw:\n%s", strings.Join(calls, "\n"))
	}
	usage, _ := os.ReadFile(filepath.Join(ts.dir, "data_usage.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(usage)), "\n")
	// The redirect within the Hub isn't counted; where it lands and the Xet
	// grant are.
	if len(lines) != 2 || !strings.Contains(lines[0], `"file":"onnx/config.json"`) ||
		!strings.Contains(lines[1], `"method":"xet"`) || !strings.Contains(lines[1], `"revision":"0a1b2c"`) {
		t.Fatalf("usage:\n%s", usage)
	}

	dataset := ts.server.issueDataToken(dataClaims{Device: device.Fingerprint, User: "alice", Scope: "*", Expires: computeNow().Add(time.Hour).Unix()})
	response = proxyCall(t, ts, http.MethodGet, "/hf/datasets/hifinab/bars/resolve/main/data/day.parquet", dataset)
	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "https://cdn.example/signed/day.parquet?Expires=1" {
		t.Fatalf("a CDN redirect: %d %q", response.StatusCode, response.Header.Get("Location"))
	}
	usage, _ = os.ReadFile(filepath.Join(ts.dir, "data_usage.jsonl"))
	if !strings.Contains(string(usage), `"file":"data/day.parquet","method":"GET","status":302,"size":5000`) {
		t.Fatalf("usage:\n%s", usage)
	}
}

func TestDataProxyRefusesEverythingElse(t *testing.T) {
	ts, hub := newDataServer(t)
	device := ts.aliceDevice(t)
	claims := dataClaims{Device: device.Fingerprint, User: "alice", Scope: "model:hifinab/ranker", Expires: computeNow().Add(time.Hour).Unix()}
	token := ts.server.issueDataToken(claims)
	expired := claims
	expired.Expires = computeNow().Add(-time.Minute).Unix()
	other := claims
	other.User = "mallory"

	cases := []struct {
		method, path, token string
		status              int
	}{
		{http.MethodGet, "/hf/api/models/hifinab/ranker", "", http.StatusUnauthorized},
		{http.MethodGet, "/hf/api/models/hifinab/ranker", "hf_team", http.StatusUnauthorized},
		{http.MethodGet, "/hf/api/models/hifinab/ranker", ts.server.issueDataToken(expired), http.StatusUnauthorized},
		{http.MethodGet, "/hf/api/models/hifinab/ranker", ts.server.issueDataToken(other), http.StatusUnauthorized},
		{http.MethodGet, "/hf/api/models/hifinab/ranker", token[:len(token)-4] + "AAAA", http.StatusUnauthorized},
		// Writes, and anything but a repository's reads.
		{http.MethodPost, "/hf/api/models/hifinab/ranker/commit/main", token, http.StatusForbidden},
		{http.MethodPost, "/hf/api/repos/create", token, http.StatusForbidden},
		{http.MethodDelete, "/hf/api/models/hifinab/ranker", token, http.StatusForbidden},
		{http.MethodPut, "/hf/hifinab/ranker/resolve/main/config.json", token, http.StatusForbidden},
		{http.MethodGet, "/hf/api/whoami-v2", token, http.StatusForbidden},
		{http.MethodGet, "/hf/api/models?author=hifinab", token, http.StatusForbidden},
		{http.MethodGet, "/hf/api/spaces/hifinab/app", token, http.StatusForbidden},
		{http.MethodGet, "/hf/api/models/hifinab/ranker/settings", token, http.StatusForbidden},
		// Climbing out of the repository, plainly or encoded.
		{http.MethodGet, "/hf/api/models/hifinab/ranker/tree/main/..%2F..%2F..%2Fbars", token, http.StatusForbidden},
		{http.MethodGet, "/hf/hifinab/ranker/resolve/main/%2E%2E/%2E%2E/bars/resolve/main/x", token, http.StatusForbidden},
		{http.MethodGet, "/hf/api/models/hifinab%2Fbars/x/revision/main", token, http.StatusForbidden},
		// Another repository than the token's.
		{http.MethodGet, "/hf/api/models/hifinab/bars", token, http.StatusForbidden},
		{http.MethodGet, "/hf/datasets/hifinab/bars/resolve/main/x", token, http.StatusForbidden},
	}
	before := len(hub.passed())
	for _, c := range cases {
		if response := proxyCall(t, ts, c.method, c.path, c.token); response.StatusCode != c.status {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, response.StatusCode, c.status)
		}
	}
	if passed := hub.passed()[before:]; len(passed) != 0 {
		t.Fatalf("refused calls reached the hub:\n%s", strings.Join(passed, "\n"))
	}

	// Policy applies to tokens already issued.
	writeTestFile(t, policyPath(ts.dir), `{"groups":{"students":{"data":["hifinab/fills"]}}}`, 0o600)
	if response := proxyCall(t, ts, http.MethodGet, "/hf/api/models/hifinab/ranker", token); response.StatusCode != http.StatusForbidden {
		t.Fatalf("a token after the policy changed: %d", response.StatusCode)
	}
}

// fakeHFCommand installs an hf that records its arguments and the endpoint and
// token it was given.
func fakeHFCommand(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "hf")
	writeTestFile(t, script, "#!/bin/sh\n{ echo \"args: $*\"; echo \"endpoint: $HF_ENDPOINT\"; echo \"token: $HF_TOKEN\"; echo \"hub token: $HUGGING_FACE_HUB_TOKEN\"; } > "+record+"\n", 0o755)
	previous := dataHFCommand
	dataHFCommand = script
	t.Cleanup(func() { dataHFCommand = previous })
	return record
}

func TestHiDataGetRunsHFThroughTheServer(t *testing.T) {
	ts, _ := newDataServer(t)
	record := fakeHFCommand(t)
	t.Setenv("HF_TOKEN", "hf_personal")
	t.Setenv("HUGGING_FACE_HUB_TOKEN", "hf_personal")
	t.Chdir(t.TempDir())

	code, _, stderr := runHi("data", "get", "hifinab/bars")
	if code != 2 || !strings.Contains(stderr, "say dataset:hifinab/bars or model:hifinab/bars") {
		t.Fatalf("an ambiguous name: code %d\n%s", code, stderr)
	}
	code, _, stderr = runHi("data", "get", "dataset:hifinab/bars", "--include", "*.parquet", "--include", "README.md", "--exclude", "old/*")
	if code != 0 {
		t.Fatalf("get: code %d\n%s", code, stderr)
	}
	data, _ := os.ReadFile(record)
	got := string(data)
	if !strings.Contains(got, "args: download hifinab/bars --repo-type dataset --local-dir data/bars --include *.parquet --include README.md --exclude old/*") ||
		!strings.Contains(got, "endpoint: "+ts.url+"/hf\n") || !strings.Contains(got, "token: "+dataTokenPrefix) ||
		strings.Contains(got, "hf_personal") {
		t.Fatalf("hf ran with:\n%s", got)
	}

	code, _, stderr = runHi("data", "get", "hifinab/ranker", "--to", "models/r", "--revision", "v2")
	data, _ = os.ReadFile(record)
	if code != 0 || !strings.Contains(string(data), "args: download hifinab/ranker --local-dir models/r --revision v2\n") {
		t.Fatalf("get a model: code %d\n%s%s", code, data, stderr)
	}

	code, _, stderr = runHi("data", "get", "hifinab/scratch", "--include", "runs/*")
	data, _ = os.ReadFile(record)
	if code != 0 || !strings.Contains(string(data), "args: buckets sync hf://buckets/hifinab/scratch data/scratch --include runs/*\n") {
		t.Fatalf("get a bucket: code %d\n%s%s", code, data, stderr)
	}
	if code, _, stderr = runHi("data", "get", "hifinab/scratch", "--revision", "v1"); code != 2 || !strings.Contains(stderr, "no revisions") {
		t.Fatalf("a bucket with a revision: code %d\n%s", code, stderr)
	}

	code, _, stderr = runHi("data", "get", "hifinab/missing")
	if code == 0 || !strings.Contains(stderr, "not among what you may download") {
		t.Fatalf("a missing repository: code %d\n%s", code, stderr)
	}
}

func TestHiDataNeedsAServerAndHF(t *testing.T) {
	newTestServer(t)
	code, _, stderr := runHi("data", "ls")
	if code == 0 || !strings.Contains(stderr, "hi connect") {
		t.Fatalf("without a server: code %d\n%s", code, stderr)
	}

	newDataServer(t)
	previous := dataHFCommand
	dataHFCommand = "hf-not-installed"
	t.Cleanup(func() { dataHFCommand = previous })
	code, _, stderr = runHi("data", "get", "hifinab/ranker")
	if code == 0 || !strings.Contains(stderr, "pip install -U huggingface_hub") {
		t.Fatalf("without hf: code %d\n%s", code, stderr)
	}
}

func TestServerDataCommandWorksWithoutARunningServer(t *testing.T) {
	newFakeHub(t)
	ts := newTestServer(t)
	writeTestFile(t, filepath.Join(ts.dir, "config.json"), `{"listen":"127.0.0.1:0"}`, 0o600)
	tokenFile := filepath.Join(t.TempDir(), "token")
	writeTestFile(t, tokenFile, "hf_team\n", 0o600)

	code, stdout, stderr := runHi("server", "data", "add", "hifinab", "--token-file", tokenFile, "--dir", ts.dir)
	if code != 0 || !strings.Contains(stdout, "✓ hifinab: 2 datasets, 2 models, 1 bucket (token of svc, read-only)") {
		t.Fatalf("add: code %d\n%s%s", code, stdout, stderr)
	}
	code, stdout, _ = runHi("server", "data", "list", "--dir", ts.dir)
	if code != 0 || !strings.Contains(stdout, "✓ hifinab: 2 datasets, 2 models, 1 bucket") {
		t.Fatalf("list: code %d\n%s", code, stdout)
	}
	code, stdout, _ = runHi("server", "data", "test", "--dir", ts.dir)
	if code != 0 || !strings.Contains(stdout, "token of svc") {
		t.Fatalf("test: code %d\n%s", code, stdout)
	}
	code, _, _ = runHi("server", "data", "remove", "hifinab", "--dir", ts.dir)
	keys, _ := os.ReadFile(filepath.Join(ts.dir, "keys.json"))
	if code != 0 || strings.Contains(string(keys), "hf_team") {
		t.Fatalf("remove: code %d, keys %s", code, keys)
	}
	if code, _, stderr = runHi("server", "data", "add", "hifinab", "--dir", ts.dir); code == 0 || !strings.Contains(stderr, "no token given") {
		t.Fatalf("add without a token: code %d\n%s", code, stderr)
	}
}

func TestPolicyRejectsBadDataPatterns(t *testing.T) {
	if _, err := parsePolicy([]byte(`{"groups":{"staff":{"data":["hifinab/["]}}}`)); err == nil {
		t.Fatal("a broken pattern was accepted")
	}
	policy, err := parsePolicy([]byte(`{"groups":{"staff":{"data":["hifinab/*","Other/x"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"hifinab/bars": true, "HIFINAB/bars": true, "other/x": true, "other/y": false, "hifinab": false} {
		if dataAllowed(policy, "staff", id) != want {
			t.Errorf("%s: want %v", id, want)
		}
	}
	if !dataAllowed(policy, "students", "any/thing") {
		t.Error("a group without a data field should read everything")
	}
}

func TestDataProxyPassesBucketReadsOnly(t *testing.T) {
	ts, hub := newDataServer(t)
	device := ts.aliceDevice(t)
	token := ts.server.issueDataToken(dataClaims{Device: device.Fingerprint, User: "alice", Scope: "bucket:hifinab/scratch",
		Expires: computeNow().Add(time.Hour).Unix()})
	for _, c := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/hf/api/buckets/hifinab/scratch", http.StatusOK},
		{http.MethodGet, "/hf/api/buckets/hifinab/scratch/tree?recursive=true", http.StatusOK},
		{http.MethodGet, "/hf/api/buckets/hifinab/scratch/xet-read-token", http.StatusOK},
		{http.MethodHead, "/hf/buckets/hifinab/scratch/resolve/runs/a.parquet", http.StatusOK},
		{http.MethodPost, "/hf/api/buckets/hifinab/scratch/paths-info", http.StatusOK},
		// Uploads, deletes, and settings are refused.
		{http.MethodPost, "/hf/api/buckets/hifinab/scratch/batch", http.StatusForbidden},
		{http.MethodGet, "/hf/api/buckets/hifinab/scratch/xet-write-token", http.StatusForbidden},
		{http.MethodDelete, "/hf/api/buckets/hifinab/scratch", http.StatusForbidden},
		{http.MethodPut, "/hf/api/buckets/hifinab/scratch/settings", http.StatusForbidden},
		{http.MethodPost, "/hf/api/buckets/hifinab/new", http.StatusForbidden},
		// The bucket list of the organization and other buckets are not for this token.
		{http.MethodGet, "/hf/api/buckets/hifinab", http.StatusForbidden},
		{http.MethodGet, "/hf/api/buckets/hifinab/other", http.StatusForbidden},
	} {
		if response := proxyCall(t, ts, c.method, c.path, token); response.StatusCode != c.status {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, response.StatusCode, c.status)
		}
	}
	for _, call := range hub.passed() {
		if strings.Contains(call, "batch") || strings.Contains(call, "write") || strings.HasPrefix(call, "DELETE") {
			t.Fatalf("a write reached the hub: %s", call)
		}
	}

	code, stdout, stderr := runHi("data", "info", "hifinab/scratch")
	if code != 0 || !strings.Contains(stdout, "bucket hifinab/scratch") || !strings.Contains(stdout, "2 files, 7.0 KB") ||
		!strings.Contains(stdout, "runs/a.parquet") || strings.Contains(stdout, " at ") {
		t.Fatalf("info: code %d\n%s%s", code, stdout, stderr)
	}
}

func TestOldClientsDontSeeBuckets(t *testing.T) {
	for agent, want := range map[string]bool{"hi/v0.24.1": false, "hi/v0.24.0": false, "hi/v0.23.9": false,
		"hi/v0.24.2": true, "hi/v0.25.0": true, "hi/v1.0.0": true, "hi/dev": true, "curl/8": true} {
		if clientKnowsBuckets(agent) != want {
			t.Errorf("%s: want %v", agent, want)
		}
	}
}
