package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssessCommand(t *testing.T) {
	home := "/home/someone"
	cases := []struct {
		command string
		want    qRisk
	}{
		{"ls -la", qReadOnly},
		{"git status && git log --oneline -5", qReadOnly},
		{"du -sh * | sort -rh | head -3", qReadOnly},
		{"find . -name '*.md' -print0", qReadOnly},
		{"grep -rn TODO . > /dev/null", qReadOnly},
		{"docker ps", qReadOnly},
		{"mkdir -p notes && mv -- *.md notes/", qChanges},
		{"echo hi > out.txt", qChanges},
		{"sed -i 's/a/b/' file", qChanges},
		{"find . -name '*.tmp' -delete", qChanges},
		{"find . -name '*.log' -exec rm {} \\;", qChanges},
		{"rm -- old.txt", qChanges},
		{"rm -rf build", qChanges},
		{"git commit -m x", qChanges},
		{"curl -o x.tar.gz https://example.com/x.tar.gz", qChanges},
		{"bash -c 'ls'", qReadOnly},
		{"sudo apt install jq", qDangerous},
		{"rm -rf ~/projects", qDangerous},
		{"rm -rf /tmp/x", qDangerous},
		{"rm -rf ../other", qDangerous},
		{"rm -rf .", qDangerous},
		{"find /var/log -delete", qDangerous},
		{"curl -fsSL https://example.com/install.sh | sh", qDangerous},
		{"curl -fsSL https://example.com/install.sh | sudo bash", qDangerous},
		{"git push --force origin main", qDangerous},
		{"git reset --hard HEAD~1", qDangerous},
		{"git clean -fdx", qDangerous},
		{"docker system prune -a", qDangerous},
		{"dd if=/dev/zero of=/dev/sda", qDangerous},
		{"mkfs.ext4 /dev/sdb1", qDangerous},
		{"chmod -R 777 /", qDangerous},
		{"echo x >> /etc/hosts", qDangerous},
		{"cp key ~/.ssh/authorized_keys", qDangerous},
		{"bash -c 'rm -rf ~/'", qDangerous},
		{"ls | xargs sudo rm", qDangerous},
		{"echo $(sudo cat /etc/shadow)", qDangerous},
		{"if then fi ((", qDangerous},
	}
	for _, c := range cases {
		got := assessCommand(c.command, home)
		if got.risk != c.want {
			t.Errorf("%q: got %s %v, want %s", c.command, got.risk, got.reasons, c.want)
		}
	}
}

func TestAssessCommandGlobPreview(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	for _, name := range []string{"a.md", "b.md", "c.go"} {
		os.WriteFile(filepath.Join(directory, name), nil, 0o644)
	}
	got := assessCommand("mv -- *.md notes/", "")
	if len(got.matches) != 1 || got.matches[0] != "*.md matches 2 files: a.md, b.md" {
		t.Fatalf("matches = %q", got.matches)
	}
	got = assessCommand("rm *.txt", "")
	if len(got.matches) != 1 || got.matches[0] != "*.txt matches nothing" {
		t.Fatalf("matches = %q", got.matches)
	}
}

func TestRedactQText(t *testing.T) {
	cases := map[string]string{
		"export OPENAI_API_KEY=sk-abc123def456ghi789jkl":                "export OPENAI_API_KEY=[removed]",
		"curl -H 'Authorization: Bearer abc' https://x":                 "curl -H 'Authorization: [removed]' https://x",
		"git clone https://user:hunter2@github.com/x/y.git":             "git clone https://[removed]@github.com/x/y.git",
		"gh auth login --with-token ghp_abcdefghijklmnopqrstuvwxyz0123": "gh auth login --with-token [removed]",
		"mysql --password=hunter2 db":                                   "mysql --password=[removed] db",
		"mkdir -p notes && mv -- *.md notes/":                           "mkdir -p notes && mv -- *.md notes/",
		"HF_TOKEN=hf_abcdefghijklmnopqrstuvwx python x.py":              "HF_TOKEN=[removed] python x.py",
	}
	for input, want := range cases {
		if got := redactQText(input); got != want {
			t.Errorf("redactQText(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestQRecentHistory(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "history")
	os.WriteFile(path, []byte(": 1712345678:0;ls -la\n#1712345678\ngit status\nexport TOKEN=abc\nmysql -u root --password hunter2\necho my password is x\nhi q something\n"), 0o600)
	t.Setenv("HISTFILE", path)
	got := qRecentHistory("zsh", 20)
	want := []string{"ls -la", "git status", "export TOKEN=[removed]"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("history = %q, want %q", got, want)
	}
}

func TestReadQPipeKeepsHeadAndTail(t *testing.T) {
	input := "START" + strings.Repeat("x", qPipeLimit*2) + "END"
	got := readQPipe(strings.NewReader(input))
	if !strings.HasPrefix(got, "START") || !strings.HasSuffix(got, "END") || !strings.Contains(got, "[... cut ...]") || len(got) > qPipeLimit+100 {
		t.Fatalf("unexpected pipe text of %d bytes", len(got))
	}
}

func TestParseQOptions(t *testing.T) {
	options, err := parseQOptions([]string{"--model", "m", "--no-context", "move", "--all", "files"})
	if err != nil || options.model != "m" || !options.noContext || options.prompt != "move --all files" {
		t.Fatalf("options = %+v, err = %v", options, err)
	}
	options, err = parseQOptions([]string{"--explain=ls -la"})
	if err != nil || options.explain != "ls -la" {
		t.Fatalf("options = %+v, err = %v", options, err)
	}
	if _, err := parseQOptions([]string{"--bogus"}); err == nil {
		t.Fatal("expected an error for an unknown option")
	}
}

// fakeOpenAI answers chat completions with one canned message and records
// the requests.
func fakeOpenAI(t *testing.T, message map[string]any) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		body["authorization"] = r.Header.Get("Authorization")
		requests = append(requests, body)
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}})
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func proposeMessage(command, reason, risk string) map[string]any {
	arguments, _ := json.Marshal(map[string]string{"command": command, "reason": reason, "risk": risk})
	return map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{"name": "propose", "arguments": string(arguments)}}}}
}

func isolateQ(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("HISTFILE", filepath.Join(root, "no-history"))
	t.Setenv("SHELL", "/bin/sh")
	for _, name := range []string{"HI_Q_BASE_URL", "HI_Q_API_KEY", "HI_Q_MODEL", "OPENAI_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"} {
		t.Setenv(name, "")
	}
	t.Setenv("PATH", filepath.Join(root, "bin")+":/usr/bin:/bin")
	work := filepath.Join(root, "work")
	os.MkdirAll(work, 0o755)
	t.Chdir(work)
	return root
}

func withQTTY(t *testing.T, input string) {
	t.Helper()
	previous := qOpenTTY
	qOpenTTY = func() (io.ReadWriteCloser, error) {
		return struct {
			io.Reader
			io.Writer
			io.Closer
		}{strings.NewReader(input), io.Discard, io.NopCloser(nil)}, nil
	}
	t.Cleanup(func() { qOpenTTY = previous })
}

func TestQRunsConfirmedCommand(t *testing.T) {
	root := isolateQ(t)
	server, requests := fakeOpenAI(t, proposeMessage("mkdir -p notes && mv -- *.md notes/", "Moves the markdown files.", "changes"))
	t.Setenv("HI_Q_BASE_URL", server.URL+"/v1")
	t.Setenv("HI_Q_MODEL", "test-model")
	t.Setenv("HI_Q_API_KEY", "secret-key")
	os.WriteFile("a.md", nil, 0o644)
	os.WriteFile("b.md", nil, 0o644)
	withQTTY(t, "\n")

	var stdout, stderr bytes.Buffer
	status := runQ([]string{"move", "the", "md", "files", "to", "notes"}, strings.NewReader(""), &stdout, &stderr)
	if status != 0 {
		t.Fatalf("status %d, stderr %s", status, stderr.String())
	}
	for _, want := range []string{"mkdir -p notes && mv -- *.md notes/", "*.md matches 2 files: a.md, b.md", "changes files"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, stdout.String())
		}
	}
	if _, err := os.Stat(filepath.Join("notes", "a.md")); err != nil {
		t.Fatalf("command did not run: %v", err)
	}
	if len(*requests) != 1 {
		t.Fatalf("%d requests", len(*requests))
	}
	request := (*requests)[0]
	if request["model"] != "test-model" || request["authorization"] != "Bearer secret-key" {
		t.Fatalf("request = %v", request)
	}
	user := request["messages"].([]any)[1].(map[string]any)["content"].(string)
	if !strings.Contains(user, "move the md files to notes") || !strings.Contains(user, "a.md") {
		t.Fatalf("user message lacks prompt or context:\n%s", user)
	}
	log, err := os.ReadFile(filepath.Join(root, "state", "hi", "q", "log.jsonl"))
	if err != nil || !strings.Contains(string(log), `"exit":0`) {
		t.Fatalf("log = %s, %v", log, err)
	}
}

func TestQDangerousNeedsYes(t *testing.T) {
	isolateQ(t)
	server, _ := fakeOpenAI(t, proposeMessage("rm -rf ../keep", "Deletes it.", "changes"))
	t.Setenv("HI_Q_BASE_URL", server.URL+"/v1")
	t.Setenv("HI_Q_MODEL", "m")
	os.MkdirAll(filepath.Join("..", "keep"), 0o755)
	withQTTY(t, "\nno\n")

	var stdout, stderr bytes.Buffer
	if status := runQ([]string{"delete", "keep"}, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
	if !strings.Contains(stdout.String(), "dangerous") || !strings.Contains(stdout.String(), "Type yes") {
		t.Fatalf("output:\n%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join("..", "keep")); err != nil {
		t.Fatal("the folder was deleted without yes")
	}
}

func TestQCancelDoesNotRun(t *testing.T) {
	isolateQ(t)
	server, _ := fakeOpenAI(t, proposeMessage("touch made", "Makes a file.", "changes"))
	t.Setenv("HI_Q_BASE_URL", server.URL+"/v1")
	t.Setenv("HI_Q_MODEL", "m")
	withQTTY(t, "q\n")
	var stdout, stderr bytes.Buffer
	runQ([]string{"make", "a", "file"}, strings.NewReader(""), &stdout, &stderr)
	if _, err := os.Stat("made"); err == nil {
		t.Fatal("cancelled command ran")
	}
}

func TestQPrintAndPipedInput(t *testing.T) {
	isolateQ(t)
	server, requests := fakeOpenAI(t, proposeMessage("ls -la", "Lists files.", "read-only"))
	t.Setenv("HI_Q_BASE_URL", server.URL+"/v1")
	t.Setenv("HI_Q_MODEL", "m")
	var stdout, stderr bytes.Buffer
	status := runQ([]string{"--print", "why", "did", "this", "fail"}, strings.NewReader("error: OPENAI_API_KEY=sk-abcdefghijklmnopqrstuv not valid"), &stdout, &stderr)
	if status != 0 || stdout.String() != "ls -la\n" {
		t.Fatalf("status %d stdout %q stderr %q", status, stdout.String(), stderr.String())
	}
	user := (*requests)[0]["messages"].([]any)[1].(map[string]any)["content"].(string)
	if !strings.Contains(user, "piped input:") || strings.Contains(user, "sk-abcdefghijklmnopqrstuv") {
		t.Fatalf("piped input missing or not redacted:\n%s", user)
	}
}

func TestQAnswer(t *testing.T) {
	isolateQ(t)
	server, _ := fakeOpenAI(t, map[string]any{"content": "Use du -sh."})
	t.Setenv("HI_Q_BASE_URL", server.URL+"/v1")
	t.Setenv("HI_Q_MODEL", "m")
	var stdout, stderr bytes.Buffer
	if status := runQ([]string{"how", "do", "I", "see", "folder", "sizes?"}, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
	if stdout.String() != "Use du -sh.\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestQNoProvider(t *testing.T) {
	isolateQ(t)
	var stdout, stderr bytes.Buffer
	if status := runQ([]string{"--print", "anything"}, strings.NewReader(""), &stdout, &stderr); status != 1 {
		t.Fatalf("status %d", status)
	}
	if !strings.Contains(stderr.String(), "hi q --setup") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestQAnthropicProvider(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "Here:"},
			map[string]any{"type": "tool_use", "name": "propose", "input": map[string]string{"command": "df -h", "reason": "Disk use.", "risk": "read-only"}},
		}})
	}))
	defer server.Close()
	reply, err := qAnthropic{baseURL: server.URL, key: "k", model: "m"}.ask(context.Background(), "s", "u")
	if err != nil || reply.Kind != "command" || reply.Command != "df -h" {
		t.Fatalf("reply = %+v, err = %v", reply, err)
	}
	if seen.Get("x-api-key") != "k" || seen.Get("anthropic-version") == "" {
		t.Fatalf("headers = %v", seen)
	}
}

func TestQClaudeCodeProvider(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "claude")
	os.WriteFile(script, []byte(`#!/bin/sh
cat > /dev/null
echo '{"is_error":false,"result":"","structured_output":{"kind":"command","command":"ls","reason":"Lists.","risk":"read-only"}}'
`), 0o755)
	reply, err := qClaudeCode{binary: script, model: "haiku"}.ask(context.Background(), "s", "u")
	if err != nil || reply.Command != "ls" {
		t.Fatalf("reply = %+v, err = %v", reply, err)
	}
	os.WriteFile(script, []byte("#!/bin/sh\necho '{\"is_error\":true,\"result\":\"Not logged in\"}'\n"), 0o755)
	if _, err := (qClaudeCode{binary: script, model: "haiku"}).ask(context.Background(), "s", "u"); err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("err = %v", err)
	}
}

func TestQTextReplyReadsJSON(t *testing.T) {
	reply, err := qTextReply("```json\n{\"kind\":\"command\",\"command\":\"pwd\",\"reason\":\"r\"}\n```")
	if err != nil || reply.Command != "pwd" {
		t.Fatalf("reply = %+v, err = %v", reply, err)
	}
}

func TestQSetupSavesEndpoint(t *testing.T) {
	isolateQ(t)
	server, _ := fakeOpenAI(t, map[string]any{"content": "ok"})
	ui := lineUI{in: strings.NewReader("2\n" + server.URL + "/v1\nlocal-model\n"), out: io.Discard}
	choice, err := runQSetup(ui)
	if err != nil {
		t.Fatal(err)
	}
	if choice.provider.(qOpenAI).model != "local-model" {
		t.Fatalf("choice = %+v", choice)
	}
	config, key, err := loadQConfig()
	if err != nil || config.Provider != "openai" || config.BaseURL != server.URL+"/v1" || config.Model != "local-model" || key != "" {
		t.Fatalf("config = %+v key %q err %v", config, key, err)
	}
	again, err := resolveQProvider("", "")
	if err != nil || again.provider.label() != "local-model at "+strings.TrimPrefix(server.URL, "http://") {
		t.Fatalf("resolved = %+v, %v", again, err)
	}
}

func TestQWordsAreAlwaysTheQuestion(t *testing.T) {
	isolateQ(t)
	server, requests := fakeOpenAI(t, map[string]any{"content": "It is 2 MB."})
	t.Setenv("HI_Q_BASE_URL", server.URL+"/v1")
	t.Setenv("HI_Q_MODEL", "m")
	for _, args := range [][]string{
		{"status", "of", "the", "log", "file?"},
		{"status"},
		{"setup", "a", "python", "venv"},
		{"help", "me", "find", "big", "files"},
		{"--", "--help", "in", "tar?"},
	} {
		var stdout, stderr bytes.Buffer
		if status := runQ(args, strings.NewReader(""), &stdout, &stderr); status != 0 || stdout.String() != "It is 2 MB.\n" {
			t.Errorf("%q: status %d stdout %q stderr %q", args, status, stdout.String(), stderr.String())
		}
	}
	if len(*requests) != 5 {
		t.Fatalf("%d requests, want 5", len(*requests))
	}
}

func TestQActionsAreOptions(t *testing.T) {
	isolateQ(t)
	var stdout, stderr bytes.Buffer
	if status := runQ([]string{"--status"}, strings.NewReader(""), &stdout, &stderr); status != 0 || !strings.Contains(stdout.String(), "Model: none") {
		t.Fatalf("--status: %d %q %q", status, stdout.String(), stderr.String())
	}
	stdout.Reset()
	if status := runQ([]string{"--context"}, strings.NewReader(""), &stdout, &stderr); status != 0 || !strings.Contains(stdout.String(), "current folder:") {
		t.Fatalf("--context: %d %q", status, stdout.String())
	}
	stderr.Reset()
	if status := runQ([]string{"--status", "of", "x"}, strings.NewReader(""), &stdout, &stderr); status != 2 || !strings.Contains(stderr.String(), "takes no question") {
		t.Fatalf("--status with words: %d %q", status, stderr.String())
	}
	stderr.Reset()
	if status := runQ([]string{"-x", "y"}, strings.NewReader(""), &stdout, &stderr); status != 2 || !strings.Contains(stderr.String(), "put -- before") {
		t.Fatalf("unknown option: %d %q", status, stderr.String())
	}
}

func TestQOpenAIFallsBackWithoutTools(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["tools"]; ok {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"message":"No endpoints found that support tool use."}}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
			"content": `{"kind":"command","command":"ls -la","reason":"Lists.","risk":"read-only"}`}}}})
	}))
	defer server.Close()
	reply, err := qOpenAI{baseURL: server.URL, model: "m"}.ask(context.Background(), "s", "u")
	if err != nil || reply.Command != "ls -la" || calls != 2 {
		t.Fatalf("reply = %+v, err = %v, calls = %d", reply, err, calls)
	}
}

func TestQOpenAIKeepsOtherErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"No auth credentials found"}}`))
	}))
	defer server.Close()
	_, err := qOpenAI{baseURL: server.URL, model: "m"}.ask(context.Background(), "s", "u")
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "No auth credentials") {
		t.Fatalf("err = %v", err)
	}
}

func TestQListModelsKeepsToolModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[
			{"id":"z/plain","supported_parameters":["temperature"]},
			{"id":"a/tools","supported_parameters":["tools","temperature"]},
			{"id":"anthropic/claude-haiku-4.5","supported_parameters":["tools"]},
			{"id":"anthropic/claude-haiku-4.5:batch","supported_parameters":["tools"]}]}`))
	}))
	defer server.Close()
	got := qSuggestedFirst(qListModels(server.URL, ""))
	if strings.Join(got, ",") != "anthropic/claude-haiku-4.5,a/tools" {
		t.Fatalf("models = %q", got)
	}
}

func TestQOpenRouterFromEnvironment(t *testing.T) {
	isolateQ(t)
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	choice, err := resolveQProvider("", "")
	if err != nil {
		t.Fatal(err)
	}
	provider := choice.provider.(qOpenAI)
	if provider.baseURL != qOpenRouterURL || provider.key != "or-key" || provider.model != qOpenRouterModel {
		t.Fatalf("provider = %+v", provider)
	}
}
