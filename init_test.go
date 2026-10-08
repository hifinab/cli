package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runInitIn runs hi init as a device that never connected to a server.
func runInitIn(t *testing.T, directory, input string, args ...string) (int, string, string) {
	t.Helper()
	isolated := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(isolated, "cache"))
	return runInitConnected(t, directory, input, args...)
}

// runInitConnected runs hi init with whatever server connection the test
// set up.
func runInitConnected(t *testing.T, directory, input string, args ...string) (int, string, string) {
	t.Helper()
	t.Chdir(directory)
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"init"}, args...), strings.NewReader(input), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestInitWritesTheTemplateAndRecordsIt(t *testing.T) {
	parent := t.TempDir()
	code, stdout, stderr := runInitIn(t, parent, "", "python", "pricing-tools", "--yes", "--no-setup")
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, stdout, stderr)
	}
	project := filepath.Join(parent, "pricing-tools")
	for _, path := range []string{"AGENTS.md", "src/pricing_tools/__init__.py", ".agents/skills/hi/SKILL.md", ".git"} {
		if _, err := os.Stat(filepath.Join(project, path)); err != nil {
			t.Errorf("missing %s", path)
		}
	}
	if target, err := os.Readlink(filepath.Join(project, ".claude/skills/hi")); err != nil || target != "../../.agents/skills/hi" {
		t.Errorf(".claude/skills/hi -> %q, %v", target, err)
	}
	if _, err := os.Stat(filepath.Join(project, ".claude/skills/hi/SKILL.md")); err != nil {
		t.Errorf("the Claude Code link does not resolve: %v", err)
	}
	data := readTestFile(t, filepath.Join(project, templateMetadataPath))
	if strings.Contains(data, parent) || strings.Contains(data, "/home/") || strings.Contains(data, "/tmp") {
		t.Errorf("template.json holds a machine path:\n%s", data)
	}
	var metadata templateMetadataFile
	if err := json.Unmarshal([]byte(data), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Template != "python" || len(metadata.Layers) != 2 || metadata.Files["AGENTS.md"].Class != "managed" ||
		metadata.Files["CLAUDE.md"].Class != "owned" || metadata.Files["pyproject.toml"].Class != "seeded" {
		t.Errorf("unexpected metadata: %+v", metadata)
	}
	if !strings.Contains(stdout, "uv sync") || !strings.Contains(stdout, "cd pricing-tools") {
		t.Errorf("the next steps are missing:\n%s", stdout)
	}

	// Running it again changes nothing and succeeds.
	code, stdout, _ = runInitIn(t, parent, "", "python", "pricing-tools", "--yes", "--no-setup")
	if code != 0 || !strings.Contains(stdout, "already applied") {
		t.Errorf("second run: exit %d\n%s", code, stdout)
	}
}

func TestInitDryRunAndCancelWriteNothing(t *testing.T) {
	parent := t.TempDir()
	code, stdout, _ := runInitIn(t, parent, "", "web", "dash", "--dry-run")
	if code != 0 || !strings.Contains(stdout, "write    package.json") || !strings.Contains(stdout, "npm ci") {
		t.Fatalf("dry run: exit %d\n%s", code, stdout)
	}
	code, _, stderr := runInitIn(t, parent, "", "web", "dash")
	if code == 0 || !strings.Contains(stderr, "--yes") {
		t.Fatalf("without a terminal or --yes: exit %d %s", code, stderr)
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 0 {
		t.Fatalf("files were written: %v", entries)
	}
}

func TestInitListsConflictsBeforeWritingAnything(t *testing.T) {
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "Makefile"), []byte("all:\n"), 0o644)
	os.WriteFile(filepath.Join(project, "README.md"), []byte("# mine\n"), 0o644)
	code, stdout, stderr := runInitIn(t, project, "", "python", ".", "--name", "mine", "--yes", "--no-setup")
	if code == 0 || !strings.Contains(stderr, "Makefile") || !strings.Contains(stderr, "README.md") {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(project, "AGENTS.md")); err == nil {
		t.Fatal("AGENTS.md was written despite conflicts")
	}
	if got := readTestFile(t, filepath.Join(project, "Makefile")); got != "all:\n" {
		t.Fatalf("Makefile was changed: %q", got)
	}
}

func TestGuidedAndDirectInitPlanTheSame(t *testing.T) {
	parent := t.TempDir()
	_, direct, _ := runInitIn(t, parent, "", "web", "dash", "--dry-run")
	// web is the last of the seven templates; the name is asked; the directory defaults to it.
	_, guided, stderr := runInitIn(t, parent, "7\ndash\n\n", "--dry-run")
	if !strings.Contains(guided, "Template  ") {
		t.Fatalf("no plan:\n%s\n%s", guided, stderr)
	}
	plan := guided[strings.Index(guided, "Template  "):]
	if plan != direct {
		t.Fatalf("plans differ:\nguided:\n%s\ndirect:\n%s\n%s", plan, direct, stderr)
	}
}

func TestInitRefusesBadInput(t *testing.T) {
	parent := t.TempDir()
	cases := [][]string{
		{"python"},
		{"python", "Bad Name"},
		{"base", "x"},
		{"nope", "x"},
		{"python", "x", "--github", "not a repo"},
		{"--update"},
	}
	for _, args := range cases {
		if code, _, _ := runInitIn(t, parent, "", append(args, "--yes", "--no-setup")...); code == 0 {
			t.Errorf("hi init %v succeeded", args)
		}
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 0 {
		t.Fatalf("files were written: %v", entries)
	}
}

func TestInitUsesALocalSourceOnTopOfBuiltinLayers(t *testing.T) {
	source := t.TempDir()
	files := map[string]string{
		"quant/layer.json":                  `{"name": "quant", "summary": "Test layer", "extends": "python", "schema": 1, "skills": ["checks"], "remove": ["tests/test_greet.py"]}`,
		"quant/notes.md":                    "# Hifin Template Name notes\n",
		"quant/.gitignore.fragment":         "results-cache/\n",
		"skills/checks/SKILL.md":            "---\nname: checks\ndescription: Test skill\n---\n\nRun the checks.\n",
		"skills/checks/references/extra.md": "more\n",
	}
	for path, content := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(source, path)), 0o755)
		os.WriteFile(filepath.Join(source, path), []byte(content), 0o644)
	}
	t.Setenv("HI_TEMPLATES_DIR", source)
	parent := t.TempDir()

	code, stdout, stderr := runInitIn(t, parent, "", "--list")
	if code != 0 || !strings.Contains(stdout, "quant") || !strings.Contains(stdout, "local") {
		t.Fatalf("list: exit %d\n%s%s", code, stdout, stderr)
	}
	code, stdout, stderr = runInitIn(t, parent, "", "quant", "alpha", "--yes", "--no-setup")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	project := filepath.Join(parent, "alpha")
	if got := readTestFile(t, filepath.Join(project, "notes.md")); got != "# Alpha notes\n" {
		t.Errorf("notes.md = %q", got)
	}
	if _, err := os.Stat(filepath.Join(project, "tests/test_greet.py")); err == nil {
		t.Error("the removed example test was written")
	}
	if !strings.Contains(readTestFile(t, filepath.Join(project, ".gitignore")), "results-cache/") {
		t.Error("the local layer's ignore rules are missing")
	}
	skill := readTestFile(t, filepath.Join(project, ".agents/skills/checks/SKILL.md"))
	if !strings.HasPrefix(skill, "---\nname: checks") || !strings.Contains(skill, skillMarker) {
		t.Errorf("the skill lacks its frontmatter or marker:\n%s", skill)
	}
	readTestFile(t, filepath.Join(project, ".agents/skills/checks/references/extra.md"))
	var metadata templateMetadataFile
	json.Unmarshal([]byte(readTestFile(t, filepath.Join(project, templateMetadataPath))), &metadata)
	if metadata.Skills["checks"].Source != "local" || metadata.Skills["hi"].Source != "builtin" || metadata.Layers[2].Source != "local" {
		t.Errorf("sources are not recorded: %+v", metadata)
	}

	// A local layer may not take a built-in name.
	os.MkdirAll(filepath.Join(source, "python"), 0o755)
	os.WriteFile(filepath.Join(source, "python/layer.json"), []byte(`{"name": "python"}`), 0o644)
	if code, _, stderr := runInitIn(t, parent, "", "--list"); code == 0 || !strings.Contains(stderr, "built-in") {
		t.Errorf("a local python layer was accepted: %d %s", code, stderr)
	}
}

func TestInitRefusesTemplatesForNewerHi(t *testing.T) {
	source := t.TempDir()
	os.MkdirAll(filepath.Join(source, "future"), 0o755)
	os.WriteFile(filepath.Join(source, "future/layer.json"), []byte(`{"name": "future", "extends": "python", "requires_hi": ">=9.0.0"}`), 0o644)
	t.Setenv("HI_TEMPLATES_DIR", source)
	previous := version
	version = "v0.16.0"
	defer func() { version = previous }()
	code, _, stderr := runInitIn(t, t.TempDir(), "", "future", "x", "--yes", "--no-setup")
	if code == 0 || !strings.Contains(stderr, "v9.0.0") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}
