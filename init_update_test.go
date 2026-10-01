package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// localTemplate is a local source with a layer on top of python, which a
// test can change between hi init and hi init --update.
type localTemplate struct {
	t   *testing.T
	dir string
}

func newLocalTemplate(t *testing.T) *localTemplate {
	t.Helper()
	source := &localTemplate{t: t, dir: t.TempDir()}
	source.write(map[string]string{
		"quant/layer.json":          `{"name": "quant", "summary": "Test", "extends": "python", "schema": 1, "files": {"owned": ["ci/owned.txt", "ci/old.txt"]}}`,
		"quant/ci/owned.txt":        "owned v1\n",
		"quant/ci/old.txt":          "dropped later\n",
		"quant/notes.md":            "# Hifin Template Name\n\nversion 1\n",
		"quant/AGENTS.md.fragment":  "\n## Quant\n\n- rule one\n",
		"quant/.gitignore.fragment": "cache-v1/\n",
	})
	return source
}

func (s *localTemplate) write(files map[string]string) {
	for name, content := range files {
		path := filepath.Join(s.dir, name)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if content == "" {
			os.Remove(path)
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			s.t.Fatal(err)
		}
	}
}

func (s *localTemplate) generate(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	t.Setenv("HI_TEMPLATES_DIR", s.dir)
	if code, stdout, stderr := runInitIn(t, parent, "", "quant", "alpha", "--yes", "--no-setup"); code != 0 {
		t.Fatalf("hi init: %d\n%s%s", code, stdout, stderr)
	}
	return filepath.Join(parent, "alpha")
}

func runInitInProject(t *testing.T, project string, args ...string) (int, string, string) {
	t.Helper()
	return runInitConnected(t, project, "", args...)
}

func TestUpdateBringsARepositoryUpToTheTemplates(t *testing.T) {
	source := newLocalTemplate(t)
	project := source.generate(t)
	appendFile(t, filepath.Join(project, "AGENTS.md"), "- our own rule\n")
	writeTestFile(t, filepath.Join(project, "notes.md"), "# Alpha\n\nversion 1\n\nour own notes\n", 0o644)

	if code, stdout, _ := runInitInProject(t, project, "--update", "--check"); code != 0 || !strings.Contains(stdout, "Nothing to update") {
		t.Fatalf("an untouched repository is behind: %d\n%s", code, stdout)
	}

	source.write(map[string]string{
		"quant/layer.json":          `{"name": "quant", "summary": "Test", "extends": "python", "schema": 1, "files": {"owned": ["ci/owned.txt", "ci/new.txt"]}}`,
		"quant/ci/owned.txt":        "owned v2\n",
		"quant/ci/old.txt":          "",
		"quant/ci/new.txt":          "brand new\n",
		"quant/notes.md":            "# Hifin Template Name\n\nversion 2\n",
		"quant/AGENTS.md.fragment":  "\n## Quant\n\n- rule one\n- rule two\n",
		"quant/.gitignore.fragment": "cache-v2/\n",
	})
	code, stdout, stderr := runInitInProject(t, project, "--update", "--check")
	if code != 1 || !strings.Contains(stdout, "write    ci/owned.txt") || !strings.Contains(stdout, "note     notes.md") ||
		!strings.Contains(stdout, "delete   ci/old.txt") || !strings.Contains(stdout, "block    AGENTS.md") {
		t.Fatalf("check: %d\n%s%s", code, stdout, stderr)
	}
	if got := readTestFile(t, filepath.Join(project, "ci/owned.txt")); got != "owned v1\n" {
		t.Fatal("--check changed a file")
	}

	code, stdout, stderr = runInitInProject(t, project, "--update", "--yes")
	if code != 0 {
		t.Fatalf("update: %d\n%s%s", code, stdout, stderr)
	}
	if got := readTestFile(t, filepath.Join(project, "ci/owned.txt")); got != "owned v2\n" {
		t.Errorf("owned file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(project, "ci/old.txt")); err == nil {
		t.Error("a dropped owned file was kept")
	}
	readTestFile(t, filepath.Join(project, "ci/new.txt"))
	agents := readTestFile(t, filepath.Join(project, "AGENTS.md"))
	if !strings.Contains(agents, "- rule two") || !strings.Contains(agents, "- our own rule") {
		t.Errorf("AGENTS.md lost the new block or the project's text:\n%s", agents)
	}
	if ignore := readTestFile(t, filepath.Join(project, ".gitignore")); !strings.Contains(ignore, "cache-v2/") || strings.Contains(ignore, "cache-v1/") {
		t.Errorf(".gitignore block not replaced:\n%s", ignore)
	}
	if got := readTestFile(t, filepath.Join(project, "notes.md")); !strings.Contains(got, "our own notes") {
		t.Errorf("a seeded file was rewritten:\n%s", got)
	}
	upgrades, _ := filepath.Glob(filepath.Join(project, "docs/upgrades/*.md"))
	if len(upgrades) != 1 {
		t.Fatalf("upgrade notes: %v", upgrades)
	}
	note := readTestFile(t, upgrades[0])
	if !strings.Contains(note, "### `notes.md`") || !strings.Contains(note, "+version 2") || !strings.Contains(note, "-our own notes") {
		t.Errorf("the upgrade note lacks the diff:\n%s", note)
	}

	// Up to date now, and running again changes nothing.
	if code, stdout, _ := runInitInProject(t, project, "--update", "--check"); code != 0 {
		t.Fatalf("still behind after updating: %d\n%s", code, stdout)
	}
}

func TestUpdateLeavesHandEditsAlone(t *testing.T) {
	source := newLocalTemplate(t)
	project := source.generate(t)
	writeTestFile(t, filepath.Join(project, "ci/owned.txt"), "changed by hand\n", 0o644)
	agents := readTestFile(t, filepath.Join(project, "AGENTS.md"))
	writeTestFile(t, filepath.Join(project, "AGENTS.md"), strings.Replace(agents, "rule one", "rule one, edited", 1), 0o644)
	source.write(map[string]string{
		"quant/ci/owned.txt":        "owned v2\n",
		"quant/AGENTS.md.fragment":  "\n## Quant\n\n- rule one\n- rule two\n",
		"quant/.gitignore.fragment": "cache-v2/\n",
	})

	code, stdout, stderr := runInitInProject(t, project, "--update", "--yes")
	if code == 0 || !strings.Contains(stderr, "ci/owned.txt") || !strings.Contains(stderr, "AGENTS.md") || !strings.Contains(stdout, "CONFLICT") {
		t.Fatalf("conflicts were not reported: %d\n%s%s", code, stdout, stderr)
	}
	if got := readTestFile(t, filepath.Join(project, "ci/owned.txt")); got != "changed by hand\n" {
		t.Error("a hand-edited owned file was overwritten")
	}
	if !strings.Contains(readTestFile(t, filepath.Join(project, ".gitignore")), "cache-v2/") {
		t.Error("files without conflicts were not updated")
	}
	// The conflict stays a conflict until it is resolved.
	if code, _, _ := runInitInProject(t, project, "--update", "--check"); code != 1 {
		t.Error("a check after a partial update passed")
	}
	if code, stdout, stderr := runInitInProject(t, project, "--update", "--yes", "--force"); code != 0 {
		t.Fatalf("--force: %d\n%s%s", code, stdout, stderr)
	}
	if got := readTestFile(t, filepath.Join(project, "ci/owned.txt")); got != "owned v2\n" {
		t.Error("--force did not overwrite")
	}
}

func TestCheckWithoutThePrivateLayers(t *testing.T) {
	source := newLocalTemplate(t)
	project := source.generate(t)
	t.Setenv("HI_TEMPLATES_DIR", "")

	code, stdout, stderr := runInitInProject(t, project, "--update", "--check")
	if code != 0 || !strings.Contains(stdout, "Not checked: the layers from local") {
		t.Fatalf("check without the private layers: %d\n%s%s", code, stdout, stderr)
	}
	// A built-in file edited by hand is still caught.
	writeTestFile(t, filepath.Join(project, "CLAUDE.md"), "something else\n", 0o644)
	if code, stdout, _ := runInitInProject(t, project, "--update", "--check"); code != 1 || !strings.Contains(stdout, "CLAUDE.md") {
		t.Fatalf("an edited built-in file was not caught: %d\n%s", code, stdout)
	}
	if code, _, stderr := runInitInProject(t, project, "--update", "--check", "--strict"); code == 0 || !strings.Contains(stderr, "can't reach") {
		t.Fatalf("--strict passed without the private layers: %s", stderr)
	}
	if code, _, stderr := runInitInProject(t, project, "--update", "--yes"); code == 0 || !strings.Contains(stderr, "can't reach") {
		t.Fatalf("an update ran without the private layers: %s", stderr)
	}
}

func TestUpdateRefusesToGoBackwards(t *testing.T) {
	project := filepath.Join(t.TempDir(), "p")
	if code, _, stderr := runInitIn(t, filepath.Dir(project), "", "python", "p", "--yes", "--no-setup"); code != 0 {
		t.Fatal(stderr)
	}
	var metadata map[string]any
	path := filepath.Join(project, templateMetadataPath)
	json.Unmarshal([]byte(readTestFile(t, path)), &metadata)
	for _, layer := range metadata["layers"].([]any) {
		layer.(map[string]any)["version"] = "hi v99.0.0"
	}
	data, _ := json.Marshal(metadata)
	writeTestFile(t, path, string(data), 0o644)
	previous := version
	version = "v0.18.0"
	defer func() { version = previous }()
	if code, _, stderr := runInitInProject(t, project, "--update", "--yes"); code == 0 || !strings.Contains(stderr, "v99.0.0") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestAdoptAddsTheTemplateAroundExistingCode(t *testing.T) {
	project := filepath.Join(t.TempDir(), "legacy-tool")
	os.MkdirAll(filepath.Join(project, "lib"), 0o755)
	writeTestFile(t, filepath.Join(project, "lib/code.py"), "print('mine')\n", 0o644)
	writeTestFile(t, filepath.Join(project, "README.md"), "# Legacy\n", 0o644)
	writeTestFile(t, filepath.Join(project, ".gitignore"), "*.log\n", 0o644)
	writeTestFile(t, filepath.Join(project, "Makefile"), "build:\n\techo build\n", 0o644)

	code, stdout, stderr := runInitIn(t, project, "", "--adopt", "python", "--yes")
	if code != 0 {
		t.Fatalf("adopt: %d\n%s%s", code, stdout, stderr)
	}
	for _, path := range []string{"AGENTS.md", "CLAUDE.md", ".claude/settings.json", ".agents/skills/hi/SKILL.md", templateMetadataPath} {
		readTestFile(t, filepath.Join(project, path))
	}
	if readTestFile(t, filepath.Join(project, "README.md")) != "# Legacy\n" || readTestFile(t, filepath.Join(project, "lib/code.py")) != "print('mine')\n" {
		t.Error("adopt changed the project's own files")
	}
	if _, err := os.Stat(filepath.Join(project, "pyproject.toml")); err == nil {
		t.Error("adopt added a starting file")
	}
	makefile := readTestFile(t, filepath.Join(project, "Makefile"))
	if !strings.Contains(makefile, "hi:begin") || !strings.Contains(makefile, "build:") {
		t.Errorf("Makefile:\n%s", makefile)
	}
	if ignore := readTestFile(t, filepath.Join(project, ".gitignore")); !strings.Contains(ignore, ".env") || !strings.Contains(ignore, "*.log") {
		t.Errorf(".gitignore:\n%s", ignore)
	}
	if !strings.Contains(stdout, "pyproject.toml") || !strings.Contains(stdout, "make check") {
		t.Errorf("adopt did not say what is missing:\n%s", stdout)
	}
	var metadata templateMetadataFile
	json.Unmarshal([]byte(readTestFile(t, filepath.Join(project, templateMetadataPath))), &metadata)
	if metadata.Params["name"] != "legacy-tool" || metadata.Template != "python" {
		t.Errorf("metadata: %+v", metadata)
	}
	// Adopted repositories update like generated ones.
	if code, stdout, _ := runInitInProject(t, project, "--update", "--check"); code != 0 {
		t.Fatalf("check after adopt: %d\n%s", code, stdout)
	}
	if code, _, stderr := runInitIn(t, project, "", "--adopt", "python", "--yes"); code == 0 || !strings.Contains(stderr, "--update") {
		t.Fatalf("adopting twice: %s", stderr)
	}
}

func TestAdoptStopsOnConflicts(t *testing.T) {
	project := filepath.Join(t.TempDir(), "busy")
	os.MkdirAll(project, 0o755)
	writeTestFile(t, filepath.Join(project, "Makefile"), "test:\n\tpytest\n", 0o644)
	writeTestFile(t, filepath.Join(project, "CLAUDE.md"), "Our own instructions.\n", 0o644)
	code, stdout, stderr := runInitIn(t, project, "", "--adopt", "python", "--yes")
	if code == 0 || !strings.Contains(stderr, "Makefile") || !strings.Contains(stderr, "CLAUDE.md") || !strings.Contains(stdout, "test targets") {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(project, "AGENTS.md")); err == nil {
		t.Fatal("files were written despite conflicts")
	}
}

func appendFile(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString(text)
	file.Close()
}

func TestInitMentionsANewerHi(t *testing.T) {
	calls := 0
	fakeReleasesWith(t, "v0.99.0", false, true, &calls)
	previous := version
	version = "v0.18.0"
	defer func() { version = previous }()
	code, _, stderr := runInitIn(t, t.TempDir(), "", "--list")
	if code != 0 || !strings.Contains(stderr, "hi v0.99.0 is out") {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	version = "v0.99.0"
	if _, _, stderr := runInitIn(t, t.TempDir(), "", "--list"); strings.Contains(stderr, "is out") {
		t.Fatalf("an up-to-date hi was told to update: %s", stderr)
	}
}
