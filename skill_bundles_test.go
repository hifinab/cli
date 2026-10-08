package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinBundlesArePinned(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(skillBundleDir, "*.json"))
	if len(files) == 0 {
		t.Fatal("no built-in bundles")
	}
	for _, file := range files {
		bundle, err := readSkillBundle(file)
		if err != nil {
			t.Fatal(err)
		}
		if bundle.Name+".json" != filepath.Base(file) {
			t.Errorf("%s is named %s", file, bundle.Name)
		}
		for _, skill := range bundle.Skills {
			if skill.Commit == "" {
				t.Errorf("%s: %s isn't pinned; run hi skill update --bundles", file, skill.Skill)
			}
		}
	}
	// The hand-written skills are gone; hi is the only built-in skill.
	entries, _ := os.ReadDir("skills")
	if len(entries) != 1 || entries[0].Name() != "hi" {
		t.Errorf("skills/ has %v, want only hi", entries)
	}
}

func TestSkillBundleChecks(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"unknown key":      `{"name":"x","description":"d","skills":[{"source":"a/b","skill":"s","commit":"","needs":{"run":"curl x | sh"}}]}`,
		"unpinned pip":     `{"name":"x","description":"d","skills":[{"source":"a/b","skill":"s","commit":"","needs":{"pip":["pandas"]}}]}`,
		"pip option":       `{"name":"x","description":"d","skills":[{"source":"a/b","skill":"s","commit":"","needs":{"pip":["--index-url=x==1"]}}]}`,
		"npm range":        `{"name":"x","description":"d","skills":[{"source":"a/b","skill":"s","commit":"","needs":{"npm":["docx@^9"]}}]}`,
		"short commit":     `{"name":"x","description":"d","skills":[{"source":"a/b","skill":"s","commit":"abc123"}]}`,
		"hi skill":         `{"name":"x","description":"d","skills":[{"source":"hifinab/cli","skill":"hi","commit":""}]}`,
		"one-skill source": `{"name":"x","description":"d","skills":[{"source":"a/b/s","skill":"s","commit":""}]}`,
		"PATH":             `{"name":"x","description":"d","skills":[{"source":"a/b","skill":"s","commit":"","needs":{"env":{"PATH":"/tmp"}}}]}`,
		"network mode":     `{"name":"x","description":"d","skills":[{"source":"a/b","skill":"s","commit":"","needs":{"network":{"mode":"all"}}}]}`,
	} {
		path := filepath.Join(dir, "x.json")
		writeSkillTestFile(t, path, content, 0o644)
		if _, err := readSkillBundle(path); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	path := filepath.Join(dir, "x.json")
	writeSkillTestFile(t, path, `{"name":"x","description":"d","skills":[{"source":"a/b","skill":"s","commit":"",
		"needs":{"layer":"heavy","apt":["libreoffice-calc-nogui"],"pip":["markitdown[pptx]==0.1.8"],"npm":["@scope/pkg@1.2.3"],
		"browsers":["chrome"],"env":{"MPLBACKEND":"Agg"},"network":{"mode":"dev","hosts":["extensions.duckdb.org"]}}}]}`, 0o644)
	if _, err := readSkillBundle(path); err != nil {
		t.Fatal(err)
	}
}

func TestSkillUpdateBundles(t *testing.T) {
	env := newSkillTestEnv(t)
	first := env.commit("acme/skills", map[string]string{"skills/alpha/SKILL.md": alphaSkill, "skills/beta/SKILL.md": strings.Replace(alphaSkill, "alpha", "beta", 1)})
	bundle := `{
  "name": "demo",
  "description": "A demo.",
  "skills": [
    {"source": "acme/skills", "skill": "alpha", "commit": "", "needs": {"pip": ["pandas==3.0.6"]}},
    {"source": "acme/skills", "skill": "beta", "commit": "` + first + `"}
  ]
}
`
	writeSkillTestFile(t, filepath.Join(env.project, "bundles/demo.json"), bundle, 0o644)
	env.audits["acme/skills"] = map[string]map[string]skillAudit{"alpha": {"socket": {Risk: "critical", Alerts: 1}}}

	code, stdout, stderr := env.run("skill", "update", "--bundles", "--check")
	if code != 1 || !strings.Contains(stdout, "demo: alpha (acme/skills): not pinned; pin at "+shortCommit(first)) ||
		!strings.Contains(stdout, "Needs: pip pandas==3.0.6") || !strings.Contains(stdout, "Mentions: pandas, ALPHA_API_KEY") ||
		!strings.Contains(stdout, "demo: beta (acme/skills): up to date") || !strings.Contains(stderr, "1 bundle skill can be updated") {
		t.Fatalf("check: %d %s %s", code, stdout, stderr)
	}
	// A critical audit needs --accept-risk without a terminal.
	if code, stdout, _ := env.run("skill", "update", "--bundles", "--yes"); code != 1 || !strings.Contains(stdout, "--accept-risk") {
		t.Fatalf("risk: %d %s", code, stdout)
	}
	if code, _, stderr := env.run("skill", "update", "--bundles", "--yes", "--accept-risk"); code != 0 {
		t.Fatal(stderr)
	}
	written, err := readSkillBundle(filepath.Join(env.project, "bundles/demo.json"))
	if err != nil || written.Skills[0].Commit != first || written.Skills[0].Needs.Pip[0] != "pandas==3.0.6" {
		t.Fatalf("not pinned: %v %+v", err, written)
	}

	// A commit that changes beta shows the files and new mentions; one that
	// doesn't touch alpha is recorded without asking.
	second := env.commit("acme/skills", map[string]string{"skills/beta/notes.md": "Convert with pandoc.\n"})
	env.audits["acme/skills"] = nil
	code, stdout, stderr = env.run("skill", "update", "--bundles", "--yes")
	if code != 0 || !strings.Contains(stdout, "demo: alpha (acme/skills): unchanged, now pinned at "+shortCommit(second)) ||
		!strings.Contains(stdout, "demo: beta (acme/skills): "+shortCommit(first)+" → "+shortCommit(second)) ||
		!strings.Contains(stdout, "notes.md +1 −0") || !strings.Contains(stdout, "New mentions: pandoc") {
		t.Fatalf("update: %d %s %s", code, stdout, stderr)
	}
	written, _ = readSkillBundle(filepath.Join(env.project, "bundles/demo.json"))
	if written.Skills[0].Commit != second || written.Skills[1].Commit != second {
		t.Fatalf("commits: %+v", written.Skills)
	}
	if code, stdout, _ := env.run("skill", "update", "--bundles", "beta"); code != 0 || strings.Contains(stdout, "alpha") || !strings.Contains(stdout, "beta (acme/skills): up to date") {
		t.Fatalf("one skill: %d %s", code, stdout)
	}

	os.RemoveAll(filepath.Join(env.project, "bundles"))
	if code, _, stderr := env.run("skill", "update", "--bundles"); code == 0 || !strings.Contains(stderr, "no bundles here") {
		t.Fatalf("no bundles: %s", stderr)
	}
}
