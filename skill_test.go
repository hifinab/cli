package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runSkillIn(t *testing.T, directory string, args ...string) (int, string, string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(previous)
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"skill"}, args...), strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestSkillWritesOnceAndLinksItForClaudeCode(t *testing.T) {
	project := t.TempDir()
	code, stdout, stderr := runSkillIn(t, project)
	if code != 0 {
		t.Fatalf("exit code = %d; stderr: %s", code, stderr)
	}
	path := filepath.Join(project, ".agents", "skills", "hi", "SKILL.md")
	content := readTestFile(t, path)
	if !strings.HasPrefix(content, "---\nname: hi\ndescription: ") {
		t.Fatalf("%s does not start with skill frontmatter", path)
	}
	if !strings.Contains(content, skillMarker+" "+version) || !strings.Contains(content, "Ask before spending") {
		t.Fatalf("%s lacks the version marker or the rules", path)
	}
	link := filepath.Join(project, ".claude", "skills", "hi")
	if target, err := os.Readlink(link); err != nil || target != "../../.agents/skills/hi" {
		t.Fatalf("%s is not a relative link to the skill: %q, %v", link, target, err)
	}
	if readTestFile(t, filepath.Join(link, "SKILL.md")) != content {
		t.Fatal("Claude Code's path does not resolve to the same skill")
	}
	if !strings.Contains(stdout, path) || !strings.Contains(stdout, link) {
		t.Fatalf("output does not name both paths:\n%s", stdout)
	}
	// Rerunning updates the file and keeps the link.
	if code, _, stderr := runSkillIn(t, project); code != 0 {
		t.Fatalf("rerun failed: %s", stderr)
	}
}

func TestSkillReplacesAFolderFromAnEarlierHiSkillWithTheLink(t *testing.T) {
	project := t.TempDir()
	old := filepath.Join(project, ".claude", "skills", "hi", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, old, "---\nname: hi\n---\n"+skillMarker+" v0.7.0 -->\n", 0o644)
	if code, _, stderr := runSkillIn(t, project); code != 0 {
		t.Fatalf("exit code = %d; stderr: %s", code, stderr)
	}
	if info, err := os.Lstat(filepath.Join(project, ".claude", "skills", "hi")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the old folder was not replaced by a link")
	}
}

func TestSkillKeepsForeignSkillsUnlessForced(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, ".claude", "skills", "hi", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, "my own hi skill\n", 0o644)
	code, _, stderr := runSkillIn(t, project)
	if code != 1 || !strings.Contains(stderr, "--force") {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if readTestFile(t, path) != "my own hi skill\n" {
		t.Fatal("a skill hi did not write was replaced")
	}
	if code, _, stderr := runSkillIn(t, project, "--force"); code != 0 || strings.Contains(readTestFile(t, path), "my own") {
		t.Fatalf("--force did not replace it: %s", stderr)
	}
}

func TestSkillGlobalAndPrint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if code, _, stderr := runSkillIn(t, t.TempDir(), "--global"); code != 0 {
		t.Fatalf("--global failed: %s", stderr)
	}
	for _, directory := range []string{".agents/skills/hi", ".claude/skills/hi"} {
		if _, err := os.Stat(filepath.Join(home, directory, "SKILL.md")); err != nil {
			t.Fatalf("--global did not provide %s: %v", directory, err)
		}
	}
	project := t.TempDir()
	code, stdout, _ := runSkillIn(t, project, "--print")
	if code != 0 || !strings.Contains(stdout, "name: hi") {
		t.Fatalf("--print: exit code = %d, output:\n%s", code, stdout)
	}
	if entries, _ := os.ReadDir(project); len(entries) != 0 {
		t.Fatal("--print wrote files")
	}
}

func TestSkillDescriptionFitsTheAgentSkillsLimit(t *testing.T) {
	for _, line := range strings.Split(string(hiSkill), "\n") {
		if description, ok := strings.CutPrefix(line, "description: "); ok {
			if len(description) > 1024 {
				t.Fatalf("description is %d characters; the limit is 1024", len(description))
			}
			return
		}
	}
	t.Fatal("no description in the skill frontmatter")
}
