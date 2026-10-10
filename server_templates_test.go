package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// templateRepo is a git repository of templates for a test.
type templateRepo struct {
	t   *testing.T
	dir string
}

func newTemplateRepo(t *testing.T) *templateRepo {
	t.Helper()
	repo := &templateRepo{t: t, dir: filepath.Join(t.TempDir(), "templates")}
	repo.git("init", "-q", "-b", "main", repo.dir)
	repo.commit(map[string]string{
		"quant/layer.json":         `{"name": "quant", "summary": "Research", "extends": "python", "schema": 1, "skills": ["validity"]}`,
		"quant/notes.md":           "# Hifin Template Name research notes\n",
		"skills/validity/SKILL.md": "---\nname: validity\ndescription: Check for lookahead\n---\n\nNo lookahead.\n",
		"docs/decisions.md":        "Not a layer.\n",
	}, "first")
	return repo
}

func (r *templateRepo) git(args ...string) string {
	r.t.Helper()
	command := exec.Command("git", append([]string{"-c", "user.name=test", "-c", "user.email=test@example.com"}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func (r *templateRepo) commit(files map[string]string, message string) string {
	r.t.Helper()
	for path, content := range files {
		target := filepath.Join(r.dir, path)
		os.MkdirAll(filepath.Dir(target), 0o755)
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git("-C", r.dir, "add", "-A")
	r.git("-C", r.dir, "commit", "-q", "-m", message)
	return r.git("-C", r.dir, "rev-parse", "HEAD")
}

func (ts *testServer) addTemplates(t *testing.T, repo *templateRepo, token string) templateSource {
	t.Helper()
	source, err := ts.server.addTemplateSource("firm", repo.dir, "", token, "admin")
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestServerTemplatesReachAConnectedDevice(t *testing.T) {
	ts := newTestServer(t)
	repo := newTemplateRepo(t)
	source := ts.addTemplates(t, repo, "secret-token-123")
	if len(source.Layers) != 1 || source.Layers[0].Name != "quant" || strings.Join(source.Skills, ",") != "validity" {
		t.Fatalf("unexpected source: %+v", source)
	}
	for _, file := range []string{"state.json", "audit.jsonl"} {
		if data, _ := os.ReadFile(filepath.Join(ts.dir, file)); strings.Contains(string(data), "secret-token-123") {
			t.Errorf("%s contains the token", file)
		}
	}

	// Before connecting, a device sees built-in templates only.
	code, stdout, stderr := runInitConnected(t, t.TempDir(), "", "--list")
	if code != 0 || listsTemplate(stdout, "quant") {
		t.Fatalf("unconnected list: %d\n%s%s", code, stdout, stderr)
	}

	ts.connectAs(t, "alice", "staff")
	code, stdout, stderr = runInitConnected(t, t.TempDir(), "", "--list")
	if code != 0 || !strings.Contains(stdout, "quant") || !strings.Contains(stdout, "firm "+shortCommit(source.Commit)) {
		t.Fatalf("connected list: %d\n%s%s", code, stdout, stderr)
	}

	parent := t.TempDir()
	code, stdout, stderr = runInitConnected(t, parent, "", "quant", "alpha", "--yes", "--no-setup")
	if code != 0 {
		t.Fatalf("hi init quant: %d\n%s%s", code, stdout, stderr)
	}
	project := filepath.Join(parent, "alpha")
	if got := readTestFile(t, filepath.Join(project, "notes.md")); got != "# Alpha research notes\n" {
		t.Errorf("notes.md = %q", got)
	}
	readTestFile(t, filepath.Join(project, "src/alpha/__init__.py"))
	if _, err := os.Stat(filepath.Join(project, "docs/decisions.md")); err == nil {
		t.Error("a file outside the layer was written")
	}
	skill := readTestFile(t, filepath.Join(project, ".agents/skills/validity/SKILL.md"))
	if !strings.Contains(skill, "from the firm templates") {
		t.Errorf("skill marker missing:\n%s", skill)
	}
	var metadata templateMetadataFile
	json.Unmarshal([]byte(readTestFile(t, filepath.Join(project, templateMetadataPath))), &metadata)
	last := metadata.Layers[len(metadata.Layers)-1]
	if last.Source != "firm" || last.Commit != source.Commit || metadata.Skills["validity"].Commit != source.Commit {
		t.Errorf("metadata does not record the source commit: %+v", metadata)
	}
	if data := readTestFile(t, filepath.Join(project, templateMetadataPath)); strings.Contains(data, ts.url) || strings.Contains(data, "127.0.0.1") {
		t.Errorf("metadata holds the server address:\n%s", data)
	}
	found := false
	for _, event := range ts.server.feed.events {
		found = found || strings.Contains(event.Text, "alice started a project from quant")
	}
	if !found {
		t.Error("the dashboard did not hear about the new project")
	}

	// With the server unreachable, the cached commit still works.
	connection, _ := loadServerConnection()
	connection.URL = "http://127.0.0.1:1"
	saveServerConnection(*connection)
	code, stdout, stderr = runInitConnected(t, t.TempDir(), "", "--list")
	if code != 0 || !strings.Contains(stdout, "cached") || !strings.Contains(stderr, "warning") {
		t.Fatalf("offline list: %d\n%s%s", code, stdout, stderr)
	}

	// Disconnecting forgets the server templates.
	if code, _, stderr := runHi("disconnect"); code != 0 {
		t.Fatalf("disconnect: %s", stderr)
	}
	if _, err := os.Stat(templateCacheDirectory()); err == nil {
		t.Error("the template cache survived hi disconnect")
	}
}

func TestDeviceRefusesBundlesNotSignedByItsServer(t *testing.T) {
	ts := newTestServer(t)
	ts.addTemplates(t, newTemplateRepo(t), "")
	ts.connectAs(t, "alice", "staff")

	// Pretend this device stored another server's key.
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	connection, _ := loadServerConnection()
	connection.ServerKey = base64.StdEncoding.EncodeToString(public)
	saveServerConnection(*connection)

	code, stdout, stderr := runInitConnected(t, t.TempDir(), "", "--list")
	if code != 0 || listsTemplate(stdout, "quant") || !strings.Contains(stderr, "not signed by this server's key") {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	if entries, _ := os.ReadDir(filepath.Join(templateCacheDirectory(), "firm")); len(entries) != 0 {
		t.Fatalf("the refused bundle was cached: %v", entries)
	}
	// And hi connect status notices the key changed.
	if code, _, stderr := runHi("connect", "status"); code == 0 || !strings.Contains(stderr, "different key") {
		t.Fatalf("connect status: %d %s", code, stderr)
	}
}

func TestServerKeepsServingTheLastGoodCommit(t *testing.T) {
	ts := newTestServer(t)
	repo := newTemplateRepo(t)
	first := ts.addTemplates(t, repo, "").Commit

	bad := repo.commit(map[string]string{"python/layer.json": `{"name": "python"}`}, "shadow a built-in")
	results, err := ts.server.syncTemplateSources("", "admin")
	if err != nil || results[0].Commit != first || !strings.Contains(results[0].Problem, "built-in") {
		t.Fatalf("bad commit %s: %+v %v", shortCommit(bad), results, err)
	}

	repo.git("-C", repo.dir, "rm", "-q", "-r", "python")
	repo.commit(map[string]string{"quant/extra.md": "more\n"}, "fix")
	good := repo.git("-C", repo.dir, "rev-parse", "HEAD")
	results, _ = ts.server.syncTemplateSources("firm", "admin")
	if results[0].Commit != good || results[0].Problem != "" || strings.Join(results[0].Commits, ",") != first+","+good {
		t.Fatalf("good commit: %+v", results[0])
	}
	audit, _ := os.ReadFile(filepath.Join(ts.dir, "audit.jsonl"))
	if !strings.Contains(string(audit), "updated template source") || !strings.Contains(string(audit), "template source problem") {
		t.Errorf("the audit log misses the change or the problem:\n%s", audit)
	}
}

func TestAddingABrokenSourceFails(t *testing.T) {
	ts := newTestServer(t)
	repo := newTemplateRepo(t)
	repo.commit(map[string]string{"quant/layer.json": `{"name": "quant", "extends": "nowhere"}`}, "break it")
	if _, err := ts.server.addTemplateSource("firm", repo.dir, "", "", "admin"); err == nil || !strings.Contains(err.Error(), "nowhere") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(ts.server.templateMirror("firm")); err == nil {
		t.Error("the failed mirror was kept")
	}
	for _, name := range []string{"builtin", "local", "Bad"} {
		if _, err := ts.server.addTemplateSource(name, repo.dir, "", "", "admin"); err == nil {
			t.Errorf("source name %q was accepted", name)
		}
	}
}

func TestPolicyLimitsTemplateSourcesPerGroup(t *testing.T) {
	ts := newTestServer(t)
	commit := ts.addTemplates(t, newTemplateRepo(t), "").Commit
	writePolicy(t, ts, `{"groups": {"students": {"template_sources": []}}}`)
	ts.connectAs(t, "chen", "students")

	code, stdout, _ := runInitConnected(t, t.TempDir(), "", "--list")
	if code != 0 || listsTemplate(stdout, "quant") {
		t.Fatalf("students see firm templates:\n%s", stdout)
	}
	key, _ := loadDeviceKey(false)
	client := newServerClient(ts.url, key)
	var bundle apiTemplateBundle
	if err := client.call("GET", "/v1/templates/firm/"+commit, nil, &bundle); err == nil {
		t.Fatal("a student downloaded the firm bundle")
	}
}

func TestServerTemplatesCommandWorksWithoutARunningServer(t *testing.T) {
	ts := newTestServer(t)
	writeTestFile(t, filepath.Join(ts.dir, "config.json"), `{"listen": "127.0.0.1:0"}`, 0o600)
	repo := newTemplateRepo(t)
	code, stdout, stderr := runHi("server", "templates", "add", "firm", repo.dir, "--dir", ts.dir)
	if code != 0 || !strings.Contains(stdout, "layers quant; skills validity") {
		t.Fatalf("add: %d\n%s%s", code, stdout, stderr)
	}
	code, stdout, _ = runHi("server", "templates", "list", "--dir", ts.dir)
	if code != 0 || !strings.Contains(stdout, "firm  "+repo.dir) {
		t.Fatalf("list: %d\n%s", code, stdout)
	}
	if code, _, _ := runHi("server", "templates", "remove", "firm", "--dir", ts.dir); code != 0 {
		t.Fatal("remove failed")
	}
	if code, stdout, _ := runHi("server", "templates", "list", "--dir", ts.dir); code != 0 || !strings.Contains(stdout, "No template sources") {
		t.Fatalf("after remove: %s", stdout)
	}
}

func TestAStuckGitDoesNotBlockTheTemplateCommands(t *testing.T) {
	ts := newTestServer(t)
	ts.addTemplates(t, newTemplateRepo(t), "")
	// A git that never answers, in front of the real one.
	bin := t.TempDir()
	writeTestFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nexec sleep 60\n", 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	previous := templateGitTimeout
	templateGitTimeout = 200 * time.Millisecond
	defer func() { templateGitTimeout = previous }()

	started := time.Now()
	results, err := ts.server.syncTemplateSources("firm", "admin")
	if err != nil || !strings.Contains(results[0].Problem, "no answer") {
		t.Fatalf("sync: %+v %v", results, err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("sync took %s", elapsed)
	}
	if err := ts.server.removeTemplateSource("firm", "admin"); err != nil {
		t.Fatal(err)
	}
}

func TestRenamingASourceKeepsItsTokenAndCommits(t *testing.T) {
	ts := newTestServer(t)
	commit := ts.addTemplates(t, newTemplateRepo(t), "secret-token-123").Commit
	ts.connectAs(t, "alice", "staff")
	if code, stdout, stderr := runInitConnected(t, t.TempDir(), "", "--list"); code != 0 || !strings.Contains(stdout, "firm ") {
		t.Fatalf("list before rename: %s%s", stdout, stderr)
	}
	if err := ts.server.renameTemplateSource("firm", "private", "admin"); err != nil {
		t.Fatal(err)
	}
	if ts.server.keys["template:private"] != "secret-token-123" || ts.server.keys["template:firm"] != "" {
		t.Fatal("the token did not move with the source")
	}
	results, err := ts.server.syncTemplateSources("private", "admin")
	if err != nil || results[0].Commit != commit || results[0].Problem != "" {
		t.Fatalf("sync after rename: %+v %v", results, err)
	}
	if err := ts.server.renameTemplateSource("private", "builtin", "admin"); err == nil {
		t.Error("renamed to a reserved name")
	}
	code, stdout, _ := runInitConnected(t, t.TempDir(), "", "--list")
	if code != 0 || !strings.Contains(stdout, "private "+shortCommit(commit)) {
		t.Fatalf("list after rename:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(templateCacheDirectory(), "firm")); err == nil {
		t.Error("the cache still holds the old name")
	}
	// The guided menu calls it a private repo, whatever the source's name.
	_, stdout, _ = runInitConnected(t, t.TempDir(), "")
	if !strings.Contains(stdout, "quant") || !strings.Contains(stdout, "(private repo)") {
		t.Fatalf("menu:\n%s", stdout)
	}
}

func TestUpdateFollowsARenamedSource(t *testing.T) {
	ts := newTestServer(t)
	ts.addTemplates(t, newTemplateRepo(t), "")
	ts.connectAs(t, "alice", "staff")
	parent := t.TempDir()
	if code, stdout, stderr := runInitConnected(t, parent, "", "quant", "alpha", "--yes", "--no-setup"); code != 0 {
		t.Fatalf("init: %s%s", stdout, stderr)
	}
	if err := ts.server.renameTemplateSource("firm", "private", "admin"); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(parent, "alpha")
	code, stdout, stderr := runInitConnected(t, project, "", "--update", "--yes")
	if code != 0 || !strings.Contains(stdout, "firm is now called private") {
		t.Fatalf("update after rename: %d\n%s%s", code, stdout, stderr)
	}
	var metadata templateMetadataFile
	json.Unmarshal([]byte(readTestFile(t, filepath.Join(project, templateMetadataPath))), &metadata)
	if metadata.Layers[len(metadata.Layers)-1].Source != "private" || metadata.Skills["validity"].Source != "private" {
		t.Fatalf("the new name was not recorded: %+v", metadata)
	}
	if code, stdout, _ := runInitConnected(t, project, "", "--update", "--check"); code != 0 || strings.Contains(stdout, "Not checked") {
		t.Fatalf("check after rename: %d\n%s", code, stdout)
	}
}

// listsTemplate reports whether hi init --list shows a template by this
// name, so a template whose name merely contains it doesn't count.
func listsTemplate(list, name string) bool {
	for _, line := range strings.Split(list, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == name {
			return true
		}
	}
	return false
}

func TestUpdateFollowsALayerThatLeftTheBuiltIns(t *testing.T) {
	ts := newTestServer(t)
	ts.addTemplates(t, newTemplateRepo(t), "")
	ts.connectAs(t, "alice", "staff")
	parent := t.TempDir()
	if code, stdout, stderr := runInitConnected(t, parent, "", "quant", "alpha", "--yes", "--no-setup"); code != 0 {
		t.Fatalf("init: %s%s", stdout, stderr)
	}
	// As if quant had been built in when the project was made, as
	// autoresearch-quant was before it moved to the private repo.
	project := filepath.Join(parent, "alpha")
	path := filepath.Join(project, templateMetadataPath)
	var metadata templateMetadataFile
	json.Unmarshal([]byte(readTestFile(t, path)), &metadata)
	last := len(metadata.Layers) - 1
	metadata.Layers[last] = templateMetadataSource{Name: "quant", Source: "builtin", Version: "hi v0.36.2"}
	data, _ := json.MarshalIndent(metadata, "", "  ")
	writeTestFile(t, path, string(data), 0o644)

	code, stdout, stderr := runInitConnected(t, project, "", "--update", "--yes")
	if code != 0 || !strings.Contains(stdout, "quant moved from the built-in templates to firm") {
		t.Fatalf("update: %d\n%s%s", code, stdout, stderr)
	}
	json.Unmarshal([]byte(readTestFile(t, path)), &metadata)
	if got := metadata.Layers[len(metadata.Layers)-1]; got.Source != "firm" || got.Commit == "" {
		t.Fatalf("the move was not recorded: %+v", got)
	}
}
