package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestBuiltinBundlesPlanAndBuild(t *testing.T) {
	all, err := loadBundleSource(bundleSource{name: "builtin", files: builtinBundleFiles})
	if err != nil || len(all) != 3 {
		t.Fatalf("built-in bundles: %v %d", err, len(all))
	}
	plan, err := planBundles([]string{"web", "data"}, all)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(plan.skillNames(), ","); got != "agent-browser,convert-file,data-visualization,hi,query,read-file" {
		t.Fatalf("skills %s", got)
	}
	if plan.network != "open" || !plan.chrome || strings.Join(plan.hosts, ",") != "extensions.duckdb.org" {
		t.Fatalf("network %s %v %v", plan.network, plan.chrome, plan.hosts)
	}
	dockerfile, err := plan.dockerfile("localhost/hi-box:abc")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FROM localhost/hi-box:abc\n",
		"ENV AGENT_BROWSER_EXECUTABLE_PATH=/opt/hi/bin/chrome\n",
		"ENV MPLBACKEND=Agg\n",
		"ENV NODE_PATH=/opt/hi/npm/lib/node_modules\n",
		"python3 -m venv /opt/hi/venv",
		"npm install -g --no-fund --no-audit --prefix /opt/hi/npm 'agent-browser@0.38.2'\n",
		"agent-browser install --with-deps",
		// duckdb-cli once, though three skills ask for it.
		"/opt/hi/venv/bin/pip install --no-cache-dir 'duckdb-cli==1.5.6'\n",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("the Dockerfile has no %q:\n%s", want, dockerfile)
		}
	}
	// heavy before light, and the same skills give the same bytes.
	if strings.Index(dockerfile, "# heavy") > strings.Index(dockerfile, "# light") {
		t.Error("light before heavy")
	}
	again, _ := planBundles([]string{"data", "web"}, all)
	if second, _ := again.dockerfile("localhost/hi-box:abc"); second != dockerfile {
		t.Error("the order of --bundle changed the image")
	}
	office, _ := planBundles([]string{"office"}, all)
	if len(office.notes) != 1 || !strings.Contains(office.notes[0], "proprietary") {
		t.Fatalf("notes %v", office.notes)
	}
	if _, err := planBundles([]string{"nope"}, all); err == nil || !strings.Contains(err.Error(), "data, office, web") {
		t.Fatalf("unknown bundle: %v", err)
	}
}

func TestBundleNetworkQuestion(t *testing.T) {
	all, _ := loadBundleSource(bundleSource{name: "builtin", files: builtinBundleFiles})
	web, _ := planBundles([]string{"web"}, all)
	data, _ := planBundles([]string{"data"}, all)

	// Without a terminal, wider needs flags.
	if _, _, err := web.widen("dev", nil, strings.NewReader(""), &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "--network open") {
		t.Fatalf("web without a terminal: %v", err)
	}
	if network, _, err := web.widen("open", nil, strings.NewReader(""), &strings.Builder{}); err != nil || network != "open" {
		t.Fatalf("web with --network open: %s %v", network, err)
	}
	if _, _, err := data.widen("dev", nil, strings.NewReader(""), &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "--allow extensions.duckdb.org") {
		t.Fatalf("data without a terminal: %v", err)
	}
	network, hosts, err := data.widen("dev", []string{"extensions.duckdb.org"}, strings.NewReader(""), &strings.Builder{})
	if err != nil || network != "dev" || len(hosts) != 1 {
		t.Fatalf("data with --allow: %s %v %v", network, hosts, err)
	}
}

func TestTeamAndLocalBundles(t *testing.T) {
	team := fstest.MapFS{
		"bundles/quant.json": {Data: []byte(`{"name":"quant","description":"Hifin's quant work.","skills":[
			{"source":".","skill":"bars","commit":"","needs":{"pip":["polars==1.30.0"]}},
			{"source":"duckdb/duckdb-skills","skill":"query","commit":"7feda8e01e22bc0886c86123f3884947e36d8c69"}]}`)},
		"skills/bars/SKILL.md": {Data: []byte("---\nname: bars\ndescription: Read bars.\n---\nUse polars.\n")},
	}
	bundles, err := loadBundleSource(bundleSource{name: "hifinab", files: team, commit: "abc"})
	if err != nil || bundles["quant"] == nil {
		t.Fatalf("team bundles: %v", err)
	}
	if _, _, err := checkTemplateSource("hifinab", team); err != nil {
		t.Fatalf("a source with only bundles and skills: %v", err)
	}
	broken := fstest.MapFS{"bundles/x.json": {Data: []byte(`{"name":"x","description":"d","skills":[{"source":".","skill":"gone","commit":""}]}`)}}
	if _, err := loadBundleSource(bundleSource{name: "hifinab", files: broken}); err == nil || !strings.Contains(err.Error(), "not in skills/") {
		t.Fatalf("a missing own skill: %v", err)
	}
	// The built-in source has no skills of its own.
	if _, err := parseSkillBundle([]byte(`{"name":"x","description":"d","skills":[{"source":".","skill":"s","commit":""}]}`), "x", false); err == nil {
		t.Fatal("a built-in bundle with its own skill")
	}

	// A team source's own skill is copied out at its commit; the box's home
	// gets it, the hi skill, and Claude Code's link.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	plan, err := planBundles([]string{"quant"}, map[string]*loadedBundle{"quant": bundles["quant"]})
	if err != nil {
		t.Fatal(err)
	}
	plan.skills = plan.skills[:1] // bars only; query would need GitHub
	if err := plan.fetchSkills(&strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := plan.installSkills(home); err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(home, ".agents/skills/bars/SKILL.md")) || !fileExists(filepath.Join(home, ".claude/skills/bars/SKILL.md")) ||
		!fileExists(filepath.Join(home, ".claude/skills/hi/SKILL.md")) {
		t.Fatal("skills missing from the box's home")
	}

	// Local bundles win over built-in ones of the same name.
	local := t.TempDir()
	writeSkillTestFile(t, filepath.Join(local, "bundles", "web.json"), `{"name":"web","description":"Mine.","skills":[{"source":".","skill":"mine","commit":""}]}`, 0o644)
	writeSkillTestFile(t, filepath.Join(local, "skills", "mine", "SKILL.md"), "---\nname: mine\ndescription: m\n---\n", 0o644)
	t.Setenv("HI_BUNDLES_DIR", local)
	t.Setenv("HOME", t.TempDir()) // no hi server connection
	all, err := loadAllBundles(&strings.Builder{})
	if err != nil || all["web"].Description != "Mine." || all["web"].source.name != "local" || all["office"] == nil {
		t.Fatalf("local bundles: %v %+v", err, all["web"])
	}
}

func TestBriefFrontMatter(t *testing.T) {
	front, body, err := splitFrontMatter("---\nagent: codex\nbundles: [web, office]\nnetwork: open\nallow: [example.com]\ndata: true\n---\n# Nordic GPUs\n\nFind them.", "brief.md")
	if err != nil || front.Agent != "codex" || strings.Join(front.Bundles, ",") != "web,office" || front.Network != "open" || !front.Data || body != "# Nordic GPUs\n\nFind them." {
		t.Fatalf("%+v %q %v", front, body, err)
	}
	for _, bad := range []string{"---\nrun: rm -rf /\n---\nx", "---\nnetwork: all\n---\nx", "---\nagent: gemini\n---\nx"} {
		if _, _, err := splitFrontMatter(bad, "brief.md"); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if front, body, _ := splitFrontMatter("# No front matter\n---\n", "brief.md"); front != nil || body != "# No front matter\n---\n" {
		t.Fatalf("a brief without front matter: %+v %q", front, body)
	}

	// Without a terminal, what widens the box is left out; flags win.
	options := boxOptions{fromBrief: front, taskFile: "brief.md"}
	var out strings.Builder
	kind, err := applyFrontMatter("", &options, strings.NewReader(""), &out)
	if err != nil || kind != "codex" || strings.Join(options.bundles, ",") != "web,office" || options.network != "" || options.data || len(options.allow) != 0 ||
		!strings.Contains(out.String(), "Ignoring network open, example.com, the team's data") {
		t.Fatalf("%s %+v %s %v", kind, options, out.String(), err)
	}
	options = boxOptions{fromBrief: front, network: "dev", bundles: []string{"data"}, data: true, allow: []string{"example.com"}}
	kind, _ = applyFrontMatter("claude", &options, strings.NewReader(""), &out)
	if kind != "claude" || options.network != "dev" || strings.Join(options.bundles, ",") != "data" {
		t.Fatalf("flags didn't win: %s %+v", kind, options)
	}
}

func TestAgentTaskFileWithFrontMatter(t *testing.T) {
	dir := t.TempDir()
	brief := filepath.Join(dir, "brief.md")
	writeTestFile(t, brief, "---\nbundles: web\n---\nFind the prices.\n", 0o644)
	options := boxOptions{words: []string{brief}}
	task, err := agentTask(&options, strings.NewReader(""))
	if err != nil || task != "Find the prices." || options.fromBrief == nil || options.fromBrief.Bundles[0] != "web" {
		t.Fatalf("%q %+v %v", task, options.fromBrief, err)
	}
	os.WriteFile(brief, []byte("---\nbundles: web\n---\n"), 0o644)
	if _, err := agentTask(&boxOptions{words: []string{brief}}, strings.NewReader("")); err == nil || !strings.Contains(err.Error(), "no task") {
		t.Fatalf("front matter only: %v", err)
	}
}
