package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeUpstream is an OpenAI-compatible endpoint that records requests.
type fakeUpstream struct {
	server   *httptest.Server
	requests []map[string]any
	auth     []string
	status   int
	reply    map[string]any
}

func newFakeUpstream(t *testing.T, command string) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{status: http.StatusOK}
	arguments, _ := json.Marshal(map[string]string{"command": command, "reason": "Does it.", "risk": "read-only"})
	f.reply = map[string]any{
		"model":   "anthropic/claude-haiku-4.5",
		"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"id": "p1", "function": map[string]any{"name": "propose", "arguments": string(arguments)}}}}}},
		"usage":   map[string]any{"prompt_tokens": 1200, "completion_tokens": 30, "cost": 0.0015},
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[{"id":"a/other","supported_parameters":["tools"]},{"id":"anthropic/claude-haiku-4.5","supported_parameters":["tools"]}]}`))
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.requests = append(f.requests, body)
		w.WriteHeader(f.status)
		if f.status != http.StatusOK {
			w.Write([]byte(`{"error":{"message":"no such model"}}`))
			return
		}
		json.NewEncoder(w).Encode(f.reply)
	}))
	t.Cleanup(f.server.Close)
	return f
}

// newAIServer is a test server with an enrolled device and a model served
// through a fake upstream.
func newAIServer(t *testing.T) (*testServer, *fakeUpstream) {
	t.Helper()
	ts := newTestServer(t)
	upstream := newFakeUpstream(t, "df -h")
	ts.server.keys[serverAIKeyName] = "team-key"
	if err := writeServerAISettings(ts.dir, serverAISettings{URL: upstream.server.URL, Model: "anthropic/claude-haiku-4.5"}); err != nil {
		t.Fatal(err)
	}
	ts.connectAs(t, "alice", "students")
	for _, name := range []string{"HI_Q_BASE_URL", "HI_Q_MODEL", "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "HI_Q_STATE", "HI_Q_SHELL"} {
		t.Setenv(name, "")
	}
	t.Setenv("HISTFILE", filepath.Join(t.TempDir(), "none"))
	var notices bytes.Buffer
	previous := qNotice
	qNotice = &notices
	t.Cleanup(func() { qNotice = previous })
	return ts, upstream
}

func askQ(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	status := runQ(args, strings.NewReader(""), &stdout, &stderr)
	return status, stdout.String(), stderr.String()
}

func TestHiQUsesTheConnectedServer(t *testing.T) {
	ts, upstream := newAIServer(t)
	status, stdout, stderr := askQ(t, "--print", "how", "full", "is", "the", "disk")
	if status != 0 || stdout != "df -h\n" {
		t.Fatalf("status %d stdout %q stderr %q", status, stdout, stderr)
	}
	if len(upstream.requests) != 1 || upstream.auth[0] != "Bearer team-key" {
		t.Fatalf("upstream saw %d requests, auth %q", len(upstream.requests), upstream.auth)
	}
	request := upstream.requests[0]
	if request["model"] != "anthropic/claude-haiku-4.5" || request["stream"] != false {
		t.Fatalf("forwarded body = %v", request)
	}
	usage, err := os.ReadFile(filepath.Join(ts.dir, "ai_usage.jsonl"))
	if err != nil || !strings.Contains(string(usage), `"user":"alice"`) || !strings.Contains(string(usage), `"cost":0.0015`) || strings.Contains(string(usage), "disk") {
		t.Fatalf("usage = %s, %v", usage, err)
	}
	if summary := ts.server.spendSummary(monthStart(computeNow()), computeNow().Add(time.Minute)); !strings.Contains(summary, "Models through hi q: $0.00 in total") || !strings.Contains(summary, "alice: $0.00, 1 requests, 1.2k tokens") {
		t.Fatalf("spend summary:\n%s", summary)
	}

	// Any model may be asked for; the server passes it on.
	askQ(t, "--model", "a/other", "--print", "again")
	if upstream.requests[1]["model"] != "a/other" {
		t.Fatalf("model = %v", upstream.requests[1]["model"])
	}

	// An upstream error is passed on and doesn't cause a fallback.
	t.Setenv("OPENAI_API_KEY", "personal")
	upstream.status = http.StatusNotFound
	status, _, stderr = askQ(t, "--model", "no/such", "--print", "again")
	if status == 0 || !strings.Contains(stderr, "no such model") {
		t.Fatalf("status %d stderr %q", status, stderr)
	}
}

func TestHiQFallsBackWhenTheServerIsDown(t *testing.T) {
	ts, upstream := newAIServer(t)
	// The first question finds the server and caches that it serves a model.
	if status, _, stderr := askQ(t, "--print", "first"); status != 0 {
		t.Fatalf("first: %s", stderr)
	}
	personal := newFakeUpstream(t, "echo personal")
	t.Setenv("OPENAI_API_KEY", "personal-key")
	t.Setenv("OPENAI_BASE_URL", personal.server.URL)
	// Now the server is gone.
	connection, _ := loadServerConnection()
	connection.URL = "http://127.0.0.1:1"
	saveServerConnection(*connection)
	saveQServerCache(qServerCache{URL: connection.URL, Checked: time.Now(), Enabled: true, Model: "m"})

	status, stdout, stderr := askQ(t, "--print", "second")
	if status != 0 || stdout != "echo personal\n" {
		t.Fatalf("status %d stdout %q stderr %q", status, stdout, stderr)
	}
	if notices := qNotice.(*bytes.Buffer).String(); !strings.Contains(notices, "127.0.0.1 can't be used") || !strings.Contains(notices, "using OPENAI_API_KEY") {
		t.Fatalf("notice = %q", notices)
	}
	// The failure is remembered, so the next question goes straight to the
	// personal key and says so.
	qNotice.(*bytes.Buffer).Reset()
	askQ(t, "--print", "third")
	if len(personal.requests) != 2 || !strings.Contains(qNotice.(*bytes.Buffer).String(), "using OPENAI_API_KEY") {
		t.Fatalf("personal requests %d, notice %q", len(personal.requests), qNotice.(*bytes.Buffer).String())
	}
	_ = ts
	_ = upstream

	// Without a personal provider, it fails and says what to do.
	t.Setenv("OPENAI_API_KEY", "")
	saveQServerCache(qServerCache{URL: connection.URL, Checked: time.Now(), Enabled: true, Model: "m"})
	status, _, stderr = askQ(t, "--print", "fourth")
	if status == 0 || !strings.Contains(stderr, "hi q --setup") {
		t.Fatalf("status %d stderr %q", status, stderr)
	}
}

func TestHiQSkipsAServerWithoutAModel(t *testing.T) {
	ts, _ := newAIServer(t)
	ts.server.mu.Lock()
	delete(ts.server.keys, serverAIKeyName)
	ts.server.mu.Unlock()
	personal := newFakeUpstream(t, "echo personal")
	t.Setenv("OPENAI_API_KEY", "personal-key")
	t.Setenv("OPENAI_BASE_URL", personal.server.URL)
	status, stdout, stderr := askQ(t, "--print", "anything")
	if status != 0 || stdout != "echo personal\n" {
		t.Fatalf("status %d stdout %q stderr %q", status, stdout, stderr)
	}
	if notices := qNotice.(*bytes.Buffer).String(); notices != "" {
		t.Fatalf("a server without a model should be skipped quietly, got %q", notices)
	}
}

func TestHiQSetupOffersTheServerFirst(t *testing.T) {
	_, upstream := newAIServer(t)
	var out bytes.Buffer
	// 1: the team's server; 1: the default model, listed first.
	choice, err := runQSetup(lineUI{in: strings.NewReader("1\n1\n"), out: &out})
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "1) Your team's hi server (127.0.0.1): no key needed (recommended)") {
		t.Fatalf("menu:\n%s", out.String())
	}
	config, key, _ := loadQConfig()
	if config.Provider != "server" || config.Model != "anthropic/claude-haiku-4.5" || key != "" {
		t.Fatalf("config %+v key %q", config, key)
	}
	if !strings.Contains(choice.provider.label(), "anthropic/claude-haiku-4.5 via 127.0.0.1") {
		t.Fatalf("label = %q", choice.provider.label())
	}
	if len(upstream.auth) < 2 || upstream.auth[0] != "Bearer team-key" {
		t.Fatalf("model list and check should use the team key: %q", upstream.auth)
	}
	if status, stdout, _ := askQ(t, "--print", "x"); status != 0 || stdout != "df -h\n" {
		t.Fatalf("saved server: %d %q", status, stdout)
	}
}

func TestServerAIRefusesWhenOff(t *testing.T) {
	ts, _ := newAIServer(t)
	connection, _ := loadServerConnection()
	key, _ := loadDeviceKey(false)
	provider := qServerProvider(connection, key, "")

	settings := readServerAISettings(ts.dir)
	settings.Off = true
	writeServerAISettings(ts.dir, settings)
	_, err := qAskOnce(t.Context(), provider, "s", "u")
	var httpErr *qHTTPError
	if !errors.As(err, &httpErr) || httpErr.code != http.StatusServiceUnavailable || !httpErr.fromServer {
		t.Fatalf("err = %v", err)
	}
}

func TestServerAIAdmin(t *testing.T) {
	ts, upstream := newAIServer(t)
	admin := httptest.NewServer(ts.server.adminHandler())
	defer admin.Close()
	post := func(body string) map[string]any {
		response, err := http.Post(admin.URL+"/admin/ai", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var out map[string]any
		json.NewDecoder(response.Body).Decode(&out)
		return out
	}
	if out := post(`{"as":"bob","off":true}`); out["on"] != false {
		t.Fatalf("off: %v", out)
	}
	if out := post(`{"as":"bob","off":false,"model":"a/other","url":"` + upstream.server.URL + `"}`); out["on"] != true || out["model"] != "a/other" {
		t.Fatalf("on: %v", out)
	}
	keys, _ := os.ReadFile(filepath.Join(ts.dir, "keys.json"))
	if !strings.Contains(string(keys), `"ai": "team-key"`) {
		t.Fatalf("keys.json = %s", keys)
	}
	audit, _ := os.ReadFile(filepath.Join(ts.dir, "audit.jsonl"))
	if !strings.Contains(string(audit), `"action":"ai off"`) || !strings.Contains(string(audit), `"action":"ai set"`) || strings.Contains(string(audit), "team-key") {
		t.Fatalf("audit = %s", audit)
	}
	request, _ := http.NewRequest(http.MethodDelete, admin.URL+"/admin/ai?as=bob", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if _, _, on := ts.server.aiConfig(); on {
		t.Fatal("still on after remove")
	}
}

func TestServerAISetCommand(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "server")
	if code, _, stderr := runHi("server", "init", "--dir", dir); code != 0 {
		t.Fatalf("init: %s", stderr)
	}
	upstream := newFakeUpstream(t, "ls")
	var stdout, stderr bytes.Buffer
	code := run([]string{"server", "ai", "set", "--dir", dir, "--url", upstream.server.URL, "--model", "a/other"}, strings.NewReader("new-key\n"), &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "hi q's default is a/other") {
		t.Fatalf("set: %d %s %s", code, stdout.String(), stderr.String())
	}
	if upstream.auth[0] != "Bearer new-key" || upstream.requests[0]["model"] != "a/other" {
		t.Fatalf("check request: %q %v", upstream.auth, upstream.requests)
	}
	settings := readServerAISettings(dir)
	keys, _ := os.ReadFile(filepath.Join(dir, "keys.json"))
	if settings.Model != "a/other" || settings.URL != upstream.server.URL || !strings.Contains(string(keys), "new-key") {
		t.Fatalf("settings %+v keys %s", settings, keys)
	}
	// A key that doesn't work is not saved.
	upstream.status = http.StatusUnauthorized
	code = run([]string{"server", "ai", "set", "--dir", dir, "--url", upstream.server.URL}, strings.NewReader("bad-key\n"), &stdout, &stderr)
	keys, _ = os.ReadFile(filepath.Join(dir, "keys.json"))
	if code == 0 || strings.Contains(string(keys), "bad-key") {
		t.Fatalf("bad key: code %d keys %s", code, keys)
	}
}

func TestModelSpendOnTheDashboard(t *testing.T) {
	ts, _ := newAIServer(t)
	if status, _, stderr := askQ(t, "--print", "x"); status != 0 {
		t.Fatal(stderr)
	}
	snapshot := ts.server.liveSnapshot("")
	if len(snapshot.Models) != 1 || snapshot.Models[0].User != "alice" || snapshot.Models[0].Requests != 1 || snapshot.ModelsMonth != 0.0015 {
		t.Fatalf("models = %+v, %v", snapshot.Models, snapshot.ModelsMonth)
	}
	if mine := ts.server.liveSnapshot("bob"); len(mine.Models) != 0 {
		t.Fatalf("bob sees %+v", mine.Models)
	}
	if line := homeModels(snapshot, true); line != "Models through hi q: $0.00 this month, 1 requests · alice $0.00" {
		t.Fatalf("home line = %q", line)
	}
	if line := homeModels(ts.server.liveSnapshot("alice"), false); !strings.HasPrefix(line, "Models through hi q:") || strings.Contains(line, "alice") {
		t.Fatalf("user home line = %q", line)
	}
}

func TestServerAIWithoutKey(t *testing.T) {
	ts, _ := newAIServer(t)
	local := newFakeUpstream(t, "uptime")
	ts.server.mu.Lock()
	delete(ts.server.keys, serverAIKeyName)
	ts.server.mu.Unlock()
	writeServerAISettings(ts.dir, serverAISettings{URL: local.server.URL, Model: "local-model", NoKey: true})
	status, stdout, stderr := askQ(t, "--print", "uptime?")
	if status != 0 || stdout != "uptime\n" {
		t.Fatalf("status %d stdout %q stderr %q", status, stdout, stderr)
	}
	if local.auth[0] != "" || local.requests[0]["model"] != "local-model" {
		t.Fatalf("auth %q body %v", local.auth, local.requests[0])
	}

	dir := filepath.Join(t.TempDir(), "server")
	runHi("server", "init", "--dir", dir)
	code, out, errOut := runHi("server", "ai", "set", "--dir", dir, "--url", local.server.URL, "--model", "local-model", "--no-key")
	if code != 0 || !strings.Contains(out, "hi q's default is local-model") || !readServerAISettings(dir).NoKey {
		t.Fatalf("set --no-key: %d %s %s", code, out, errOut)
	}
}
