package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// skillTestEnv is a project folder, git repositories standing in for
// GitHub, and a server standing in for skills.sh.
type skillTestEnv struct {
	t       *testing.T
	project string
	repos   string
	audits  map[string]map[string]map[string]skillAudit
}

func newSkillTestEnv(t *testing.T) *skillTestEnv {
	root := t.TempDir()
	env := &skillTestEnv{t: t, project: filepath.Join(root, "project"), repos: filepath.Join(root, "repos"),
		audits: map[string]map[string]map[string]skillAudit{}}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("DISABLE_TELEMETRY", "")
	t.Setenv("HI_SKILL_GIT_BASE", "file://"+env.repos+"/")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/search":
			json.NewEncoder(w).Encode(map[string]any{"skills": []skillHit{
				{ID: "copycat/skills/alpha", Source: "copycat/skills", SkillID: "alpha", Name: "alpha", Installs: 98000},
				{ID: "anthropics/skills/pdf", Source: "anthropics/skills", SkillID: "pdf", Name: "pdf", Installs: 1_260_000},
				{ID: "mintlify.com/mintlify", Source: "mintlify.com", SkillID: "mintlify", Name: "mintlify", Installs: 5},
			}})
		case "/tele/audit":
			json.NewEncoder(w).Encode(env.audits[r.URL.Query().Get("source")])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("HI_SKILLS_SH_URL", server.URL)
	os.MkdirAll(env.project, 0o755)
	t.Chdir(env.project)
	return env
}

func (e *skillTestEnv) git(dir string, args ...string) string {
	e.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
	if err != nil {
		e.t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes files into a repository, owner/repo, and commits them.
func (e *skillTestEnv) commit(repo string, files map[string]string) string {
	e.t.Helper()
	dir := filepath.Join(e.repos, repo+".git")
	if _, err := os.Stat(dir); err != nil {
		os.MkdirAll(dir, 0o755)
		e.git(dir, "init", "-q", "-b", "main")
	}
	for path, content := range files {
		if content == "" {
			os.Remove(filepath.Join(dir, path))
			continue
		}
		writeSkillTestFile(e.t, filepath.Join(dir, path), content, 0o644)
	}
	e.git(dir, "add", "-A")
	e.git(dir, "commit", "-q", "-m", "change")
	return e.git(dir, "rev-parse", "HEAD")
}

func (e *skillTestEnv) run(args ...string) (int, string, string) {
	e.t.Helper()
	var stdout, stderr strings.Builder
	code := run(args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func (e *skillTestEnv) lock() map[string]map[string]any {
	e.t.Helper()
	var lock struct {
		Skills map[string]map[string]any `json:"skills"`
	}
	if err := json.Unmarshal([]byte(readTestFile(e.t, filepath.Join(e.project, skillLockFile))), &lock); err != nil {
		e.t.Fatal(err)
	}
	return lock.Skills
}

const alphaSkill = "---\nname: alpha\ndescription: >\n  Does alpha things\n  with care.\nlicense: MIT\n---\n\nUse pandas. Set ALPHA_API_KEY.\n"

func TestSkillAddFromARepository(t *testing.T) {
	env := newSkillTestEnv(t)
	commit := env.commit("acme/skills", map[string]string{
		"skills/alpha/SKILL.md":       alphaSkill,
		"skills/alpha/scripts/run.py": "print('hi')\n",
		"skills/beta/SKILL.md":        "---\nname: beta\ndescription: \"Beta: the second\"\n---\nBeta.\n",
		"README.md":                   "not a skill\n",
	})
	env.audits["acme/skills"] = map[string]map[string]skillAudit{"alpha": {"socket": {Risk: "safe", AnalyzedAt: "2026-09-15T00:00:00Z"}}}

	// A source with several skills needs a choice without a terminal.
	code, _, stderr := env.run("skill", "add", "acme/skills")
	if code != 2 || !strings.Contains(stderr, "acme/skills has 2 skills: alpha, beta; choose with --skill") {
		t.Fatalf("no choice: %d %s", code, stderr)
	}
	// And a confirmation.
	code, _, stderr = env.run("skill", "add", "acme/skills", "--skill", "alpha")
	if code != 1 || !strings.Contains(stderr, "rerun with --yes") {
		t.Fatalf("no confirmation: %d %s", code, stderr)
	}
	code, stdout, stderr := env.run("skills", "add", "acme/skills", "--skill", "alpha", "--yes")
	if code != 0 {
		t.Fatalf("add: %d %s", code, stderr)
	}
	for _, want := range []string{"alpha from acme/skills at " + shortCommit(commit), "Does alpha things with care.", "License: MIT · 2 files, 1 script (Python)",
		"Audits: Socket safe (2026-09-15)", "Mentions: pandas, ALPHA_API_KEY", "Installed alpha in .agents/skills"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
	if readTestFile(t, filepath.Join(env.project, ".agents/skills/alpha/scripts/run.py")) != "print('hi')\n" {
		t.Fatal("the skill's files weren't copied")
	}
	if target, _ := os.Readlink(filepath.Join(env.project, ".claude/skills/alpha")); target != "../../.agents/skills/alpha" {
		t.Fatalf("link = %q", target)
	}
	entry := env.lock()["alpha"]
	hash, _ := skillFolderHash(filepath.Join(env.project, ".agents/skills/alpha"))
	if entry["source"] != "acme/skills" || entry["sourceType"] != "github" || entry["commit"] != commit ||
		entry["skillPath"] != "skills/alpha/SKILL.md" || entry["computedHash"] != hash || entry["license"] != "MIT" {
		t.Fatalf("lock entry = %v", entry)
	}

	// owner/repo/skill, as skills.sh shows it.
	if code, _, stderr := env.run("skill", "add", "acme/skills/beta", "--yes"); code != 0 {
		t.Fatalf("add beta: %s", stderr)
	}
	if code, _, stderr := env.run("skill", "add", "acme/skills/gamma", "--yes"); code == 0 || !strings.Contains(stderr, "has no skill gamma; it has alpha, beta") {
		t.Fatalf("missing skill: %s", stderr)
	}
	// A folder hi didn't write is left alone.
	writeSkillTestFile(t, filepath.Join(env.project, ".agents/skills/mine/SKILL.md"), "---\nname: mine\n---\n", 0o644)
	env.commit("other/skills", map[string]string{"mine/SKILL.md": "---\nname: mine\ndescription: x\n---\n"})
	if code, _, stderr := env.run("skill", "add", "other/skills", "--yes"); code == 0 || !strings.Contains(stderr, "wasn't installed by hi skill") {
		t.Fatalf("foreign skill: %s", stderr)
	}
	code, stdout, _ = env.run("skill", "ls")
	for _, want := range []string{"alpha  acme/skills  " + shortCommit(commit) + "  pinned", "mine   -", "not installed by hi skill"} {
		if code != 0 || !strings.Contains(stdout, want) {
			t.Errorf("ls lacks %q:\n%s", want, stdout)
		}
	}
}

func TestSkillAddAsksAboutRiskyAudits(t *testing.T) {
	env := newSkillTestEnv(t)
	env.commit("acme/skills", map[string]string{"skills/alpha/SKILL.md": alphaSkill})
	env.audits["acme/skills"] = map[string]map[string]skillAudit{"alpha": {
		"ath": {Risk: "safe"}, "socket": {Risk: "critical", Alerts: 2}}}
	code, _, stderr := env.run("skill", "add", "acme/skills", "--yes")
	if code == 0 || !strings.Contains(stderr, "alpha is rated Socket critical on skills.sh") || !strings.Contains(stderr, "--accept-risk") {
		t.Fatalf("risky add: %d %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(env.project, ".agents/skills/alpha")); err == nil {
		t.Fatal("a risky skill was installed")
	}
	if code, stdout, stderr := env.run("skill", "add", "acme/skills", "--yes", "--accept-risk"); code != 0 || !strings.Contains(stdout, "Socket critical (2 alerts)") {
		t.Fatalf("accepted: %d %s %s", code, stdout, stderr)
	}
	// DO_NOT_TRACK skips the lookup, and says so.
	t.Setenv("DO_NOT_TRACK", "1")
	env.commit("acme/more", map[string]string{"SKILL.md": "---\nname: solo\ndescription: one\n---\n"})
	if code, stdout, _ := env.run("skill", "add", "acme/more", "--yes"); code != 0 || !strings.Contains(stdout, "Audits: not looked up (DO_NOT_TRACK)") {
		t.Fatalf("DO_NOT_TRACK: %s", stdout)
	}
}

func TestSkillUpdateShowsAndMovesToNewCommits(t *testing.T) {
	env := newSkillTestEnv(t)
	first := env.commit("acme/skills", map[string]string{"skills/alpha/SKILL.md": alphaSkill, "skills/alpha/notes.md": "one\n"})
	if code, _, stderr := env.run("skill", "add", "acme/skills", "--yes"); code != 0 {
		t.Fatal(stderr)
	}
	// A commit that doesn't touch the skill is recorded without asking.
	second := env.commit("acme/skills", map[string]string{"README.md": "docs\n"})
	if code, stdout, stderr := env.run("skill", "update"); code != 0 || !strings.Contains(stdout, "alpha: unchanged, now pinned at "+shortCommit(second)) {
		t.Fatalf("unchanged: %d %s %s", code, stdout, stderr)
	}
	third := env.commit("acme/skills", map[string]string{"skills/alpha/notes.md": "one\ntwo\nthree\n"})
	code, stdout, stderr := env.run("skill", "update", "--check")
	if code != 1 || !strings.Contains(stdout, "alpha (acme/skills): "+shortCommit(second)+" → "+shortCommit(third)) ||
		!strings.Contains(stdout, "notes.md +2 −0") || !strings.Contains(stderr, "1 skill can be updated") {
		t.Fatalf("check: %d %s %s", code, stdout, stderr)
	}
	// Without a terminal, an update needs --yes.
	if code, stdout, _ := env.run("skill", "update"); code != 1 || !strings.Contains(stdout, "Skipped: rerun with --yes") {
		t.Fatalf("no --yes: %d %s", code, stdout)
	}
	if code, _, stderr := env.run("skill", "update", "--yes"); code != 0 {
		t.Fatalf("update: %s", stderr)
	}
	if readTestFile(t, filepath.Join(env.project, ".agents/skills/alpha/notes.md")) != "one\ntwo\nthree\n" || env.lock()["alpha"]["commit"] != third {
		t.Fatalf("not updated: %v", env.lock()["alpha"])
	}
	_ = first

	// Changes made here are kept unless --force.
	writeSkillTestFile(t, filepath.Join(env.project, ".agents/skills/alpha/notes.md"), "mine\n", 0o644)
	if _, stdout, _ := env.run("skill", "ls"); !strings.Contains(stdout, "changed here since it was installed") {
		t.Fatalf("ls: %s", stdout)
	}
	fourth := env.commit("acme/skills", map[string]string{"skills/alpha/notes.md": "four\n"})
	if code, stdout, _ := env.run("skill", "update", "--yes"); code != 1 || !strings.Contains(stdout, "Skipped: it was changed here") {
		t.Fatalf("local changes: %d %s", code, stdout)
	}
	if code, _, stderr := env.run("skill", "update", "--yes", "--force"); code != 0 || env.lock()["alpha"]["commit"] != fourth {
		t.Fatalf("forced: %s", stderr)
	}
	if code, _, stderr := env.run("skill", "update", "nope"); code == 0 || !strings.Contains(stderr, "nope wasn't installed by hi skill") {
		t.Fatalf("unknown: %s", stderr)
	}
}

func TestSkillLockSharedWithNpxSkills(t *testing.T) {
	env := newSkillTestEnv(t)
	commit := env.commit("acme/skills", map[string]string{"skills/alpha/SKILL.md": alphaSkill})
	// npx skills installed alpha: no commit, and a field hi doesn't know.
	writeSkillTestFile(t, filepath.Join(env.project, ".agents/skills/alpha/SKILL.md"), alphaSkill, 0o644)
	hash, _ := skillFolderHash(filepath.Join(env.project, ".agents/skills/alpha"))
	lock := `{"version": 1, "skills": {"alpha": {"source": "acme/skills", "sourceType": "github", "ref": "main",
  "skillPath": "skills/alpha/SKILL.md", "computedHash": "` + hash + `", "subagents": ["x"]}}}`
	writeSkillTestFile(t, filepath.Join(env.project, skillLockFile), lock, 0o644)
	if _, stdout, _ := env.run("skill", "ls"); !strings.Contains(stdout, "not pinned; hi skill update pins it") {
		t.Fatalf("ls: %s", stdout)
	}
	if code, stdout, stderr := env.run("skill", "update"); code != 0 || !strings.Contains(stdout, "alpha: unchanged, now pinned at "+shortCommit(commit)) {
		t.Fatalf("pin: %d %s %s", code, stdout, stderr)
	}
	entry := env.lock()["alpha"]
	if entry["commit"] != commit || entry["ref"] != "main" || entry["subagents"] == nil {
		t.Fatalf("entry = %v", entry)
	}
}

func TestSkillRemove(t *testing.T) {
	env := newSkillTestEnv(t)
	env.commit("acme/skills", map[string]string{"skills/alpha/SKILL.md": alphaSkill})
	env.run("skill", "add", "acme/skills", "--yes")
	writeSkillTestFile(t, filepath.Join(env.project, ".agents/skills/mine/SKILL.md"), "x", 0o644)
	if code, _, stderr := env.run("skill", "rm", "mine", "--yes"); code == 0 || !strings.Contains(stderr, "wasn't installed by hi skill") {
		t.Fatalf("foreign: %s", stderr)
	}
	if code, _, stderr := env.run("skill", "rm", "alpha", "--yes"); code != 0 {
		t.Fatal(stderr)
	}
	if skillInstalled(env.project, "alpha") || env.lock()["alpha"] != nil {
		t.Fatal("alpha is still there")
	}
	if code, _, stderr := env.run("skill", "rm", "alpha", "--yes"); code == 0 || !strings.Contains(stderr, "isn't installed here") {
		t.Fatalf("again: %s", stderr)
	}
}

func TestSkillAddFromAFolderAndHi(t *testing.T) {
	env := newSkillTestEnv(t)
	writeSkillTestFile(t, filepath.Join(env.project, "team-skills/style/SKILL.md"), "---\nname: style\ndescription: House style\n---\n", 0o644)
	if code, stdout, stderr := env.run("skill", "add", "./team-skills", "--yes"); code != 0 || !strings.Contains(stdout, "Audits: none (not a GitHub repository") {
		t.Fatalf("folder: %d %s %s", code, stdout, stderr)
	}
	if entry := env.lock()["style"]; entry["source"] != "./team-skills" || entry["sourceType"] != "local" || entry["commit"] != nil {
		t.Fatalf("entry = %v", entry)
	}
	writeSkillTestFile(t, filepath.Join(env.project, "team-skills/style/SKILL.md"), "---\nname: style\ndescription: House style, v2\n---\n", 0o644)
	if code, stdout, _ := env.run("skill", "update", "--yes"); code != 0 || !strings.Contains(stdout, "style (./team-skills): the folder's files changed") {
		t.Fatalf("folder update: %s", stdout)
	}
	if code, stdout, _ := env.run("skill", "add", "hi"); code != 0 || !strings.Contains(stdout, ".agents/skills/hi/SKILL.md") {
		t.Fatalf("hi: %s", stdout)
	}
	if _, stdout, _ := env.run("skill", "ls"); !strings.Contains(stdout, "hi     built in") {
		t.Fatalf("ls: %s", stdout)
	}
}

func TestSkillFind(t *testing.T) {
	env := newSkillTestEnv(t)
	env.audits["anthropics/skills"] = map[string]map[string]skillAudit{"pdf": {"ath": {Risk: "safe"}, "snyk": {Risk: "medium"}}}
	code, stdout, stderr := env.run("skill", "find", "pdf")
	if code != 0 {
		t.Fatal(stderr)
	}
	lines := strings.Split(stdout, "\n")
	// Most installed first; the maker marked; sources skills.sh can't
	// install from left out.
	if !strings.HasPrefix(lines[1], "pdf    anthropics/skills ✓  1.3M      safe medium") || !strings.HasPrefix(lines[2], "alpha  copycat/skills       98k       no audit") ||
		strings.Contains(stdout, "mintlify") {
		t.Fatalf("find:\n%s", stdout)
	}
	if code, _, stderr := env.run("skill", "find", "p"); code != 2 || !strings.Contains(stderr, "at least two characters") {
		t.Fatalf("short: %s", stderr)
	}
	t.Setenv("DO_NOT_TRACK", "1")
	if code, _, stderr := env.run("skill", "find", "pdf"); code != 1 || !strings.Contains(stderr, "DO_NOT_TRACK") {
		t.Fatalf("off: %s", stderr)
	}
}

func TestParseSkillSource(t *testing.T) {
	t.Setenv("HI_SKILL_GIT_BASE", "")
	dir := t.TempDir()
	t.Chdir(dir)
	os.Mkdir("local", 0o755)
	for _, test := range []struct {
		in   string
		want skillSource
	}{
		{"anthropics/skills", skillSource{Kind: "github", Source: "anthropics/skills", URL: "https://github.com/anthropics/skills.git"}},
		{"anthropics/skills/xlsx", skillSource{Kind: "github", Source: "anthropics/skills", URL: "https://github.com/anthropics/skills.git", Skill: "xlsx"}},
		{"https://github.com/acme/skills/tree/dev/skills/web", skillSource{Kind: "github", Source: "acme/skills", URL: "https://github.com/acme/skills.git", Ref: "dev", Path: "skills/web"}},
		{"https://github.com/acme/skills.git", skillSource{Kind: "github", Source: "acme/skills", URL: "https://github.com/acme/skills.git"}},
		{"https://gitlab.com/org/skills", skillSource{Kind: "git", Source: "https://gitlab.com/org/skills", URL: "https://gitlab.com/org/skills"}},
		{"git@github.com:acme/skills.git", skillSource{Kind: "git", Source: "git@github.com:acme/skills.git", URL: "git@github.com:acme/skills.git"}},
		{"./local", skillSource{Kind: "local", Source: filepath.Join(dir, "local")}},
		{"hi", skillSource{Kind: "builtin", Source: "hi"}},
	} {
		got, err := parseSkillSource(test.in)
		if err != nil || got != test.want {
			t.Errorf("%s: %+v, %v", test.in, got, err)
		}
	}
	for _, bad := range []string{"", "just-a-word", "./missing", "a/b/c/d"} {
		if _, err := parseSkillSource(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestSkillSelector(t *testing.T) {
	env := newSkillTestEnv(t)
	commit := env.commit("acme/skills", map[string]string{"skills/alpha/SKILL.md": alphaSkill})
	env.run("skill", "add", "acme/skills", "--yes")
	newer := env.commit("acme/skills", map[string]string{"skills/alpha/notes.md": "new\n"})
	_ = commit
	services := skillSelectorServices{
		search: func(query string) ([]skillHit, error) {
			return []skillHit{{Source: "anthropics/skills", SkillID: "pdf", Name: "pdf", Installs: 2000},
				{Source: "anthropics/skills", SkillID: "docx", Name: "docx", Installs: 1000}}, nil
		},
		audits: func(source string, skills []string) (map[string]map[string]skillAudit, error) {
			return map[string]map[string]skillAudit{"pdf": {"snyk": {Risk: "low"}}}, nil
		},
		page: func(source, skill string) (map[string]string, error) {
			return map[string]string{"description": "Work with " + skill + " files", "license": "Proprietary"}, nil
		},
		resolve: resolveSkillCommit,
	}
	m, err := newSkillSelector(false, services)
	if err != nil {
		t.Fatal(err)
	}
	var runCommand func(command tea.Cmd)
	runCommand = func(command tea.Cmd) {
		// Run a command's messages, as Bubble Tea would.
		if command == nil {
			return
		}
		message := command()
		if batch, ok := message.(tea.BatchMsg); ok {
			for _, c := range batch {
				runCommand(c)
			}
			return
		}
		if message != nil {
			_, next := m.Update(message)
			runCommand(next)
		}
	}
	send := func(message tea.Msg) {
		_, command := m.Update(message)
		runCommand(command)
	}
	keys := func(text string) {
		for _, r := range text {
			if r == ' ' {
				send(tea.KeyMsg{Type: tea.KeySpace})
			} else {
				send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			}
		}
	}
	runCommand(m.Init())
	// Before a search: what's installed, with its newer commit, and the
	// suggestions.
	view := m.View()
	if !strings.Contains(view, "✓ alpha") || !strings.Contains(view, "newer commit "+shortCommit(newer)) ||
		!strings.Contains(view, "Suggestions") || !strings.Contains(view, "agent-browser") ||
		!strings.Contains(view, "◻ hi                     built in") {
		t.Fatalf("first view:\n%s", view)
	}
	keys("pdf")
	if m.query != "pdf" || len(m.results) != 2 {
		t.Fatalf("query %q, results %v", m.query, m.results)
	}
	// Down past the installed skill to the first result, which shows its
	// details; space picks it, and a letter goes back to the search.
	send(tea.KeyMsg{Type: tea.KeyDown})
	send(tea.KeyMsg{Type: tea.KeyDown})
	view = m.View()
	if !strings.Contains(view, "Work with pdf files") || !strings.Contains(view, "License: Proprietary") || !strings.Contains(view, "Snyk low") {
		t.Fatalf("details:\n%s", view)
	}
	keys(" ")
	send(tea.KeyMsg{Type: tea.KeyDown})
	keys(" ")
	if len(m.picked) != 2 || !strings.Contains(m.View(), "2 picked") {
		t.Fatalf("picked %v", m.picked)
	}
	send(tea.KeyMsg{Type: tea.KeyEnter})
	if m.action.kind != "install" || len(m.action.hits) != 2 || m.action.hits[0].SkillID != "docx" {
		t.Fatalf("action = %+v", m.action)
	}

	// u on an installed skill updates it.
	m, _ = newSkillSelector(false, services)
	send(tea.KeyMsg{Type: tea.KeyDown})
	keys("u")
	if m.action.kind != "update" || m.action.name != "alpha" {
		t.Fatalf("update action = %+v", m.action)
	}
	// The hi skill, offered when it's missing, is written as hi skill does.
	var out strings.Builder
	if err := runSkillSelectorAction(skillSelectorAction{kind: "install", hits: []skillHit{{Source: "hi", SkillID: "hi"}}}, strings.NewReader(""), &out); err != nil ||
		hiSkillVersion(env.project) != version {
		t.Fatalf("hi: %v %s", err, out.String())
	}
	// In the search box, u is a letter.
	m, _ = newSkillSelector(false, services)
	keys("ux")
	if m.query != "ux" || m.action.kind != "" {
		t.Fatalf("typed %q, action %+v", m.query, m.action)
	}
}

func writeSkillTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	writeTestFile(t, path, content, mode)
}
