package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func composeBuiltin(t *testing.T, name, project string) *composedTemplate {
	t.Helper()
	layers, err := builtinTemplateLayers()
	if err != nil {
		t.Fatal(err)
	}
	composed, err := composeTemplate(layers, name, map[string]string{"name": project})
	if err != nil {
		t.Fatal(err)
	}
	return composed
}

func TestBuiltinTemplatesReplaceEverySentinel(t *testing.T) {
	for _, name := range []string{"python", "web", "service", "pipeline", "ml"} {
		composed := composeBuiltin(t, name, "pricing-tools")
		for path, data := range composed.Files {
			if strings.Contains(strings.ToLower(path), "hifin") || strings.Contains(strings.ToLower(string(data)), "hifin") {
				t.Errorf("%s: %s still contains a template sentinel", name, path)
			}
			if path == layerManifestName || strings.HasSuffix(path, fragmentSuffix) {
				t.Errorf("%s: %s should not be written to the project", name, path)
			}
		}
		for _, path := range []string{"AGENTS.md", "CLAUDE.md", ".claude/settings.json", ".gitignore", "Makefile", "README.md", ".github/workflows/check.yml"} {
			if _, ok := composed.Files[path]; !ok {
				t.Errorf("%s: missing %s", name, path)
			}
		}
		if got := string(composed.Files["CLAUDE.md"]); got != "@AGENTS.md\n" {
			t.Errorf("%s: CLAUDE.md = %q", name, got)
		}
		if !strings.Contains(string(composed.Files["README.md"]), "# Pricing Tools") {
			t.Errorf("%s: README.md lacks the project title", name)
		}
		if len(composed.Commands["check"]) == 0 || len(composed.Commands["setup"]) == 0 {
			t.Errorf("%s: missing setup or check commands: %v", name, composed.Commands)
		}
		if strings.Join(composed.Skills, ",") != "hi" {
			t.Errorf("%s: skills = %v", name, composed.Skills)
		}
	}
}

func TestPythonTemplateNamesThePackage(t *testing.T) {
	composed := composeBuiltin(t, "python", "pricing-tools")
	if _, ok := composed.Files["src/pricing_tools/__init__.py"]; !ok {
		t.Fatal("src/pricing_tools/__init__.py is missing")
	}
	if !strings.Contains(string(composed.Files["pyproject.toml"]), `name = "pricing-tools"`) {
		t.Fatal("pyproject.toml does not name the project")
	}
	if !strings.Contains(string(composed.Files["uv.lock"]), `name = "pricing-tools"`) {
		t.Fatal("uv.lock does not name the project")
	}
}

func TestFragmentsLandInsideTheManagedBlock(t *testing.T) {
	for name, want := range map[string]string{"python": "__pycache__/", "web": "node_modules/"} {
		composed := composeBuiltin(t, name, "demo")
		ignore := string(composed.Files[".gitignore"])
		if !strings.Contains(ignore, ".env\n") || strings.Index(ignore, want) > strings.Index(ignore, "# hi:end") {
			t.Errorf("%s: .gitignore fragment is not inside the managed block:\n%s", name, ignore)
		}
		agents := string(composed.Files["AGENTS.md"])
		if strings.Index(agents, "make check") > strings.Index(agents, "<!-- hi:end -->") || strings.Count(agents, "hi:end") != 1 {
			t.Errorf("%s: AGENTS.md managed block is malformed:\n%s", name, agents)
		}
	}
}

func TestComposeRefusesHiddenLayersAndBadNames(t *testing.T) {
	layers, err := builtinTemplateLayers()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := composeTemplate(layers, "base", map[string]string{"name": "demo"}); err == nil {
		t.Error("composing the hidden base layer succeeded")
	}
	if _, err := composeTemplate(layers, "python", map[string]string{"name": "Bad Name"}); err == nil {
		t.Error("an invalid project name was accepted")
	}
	if _, err := composeTemplate(layers, "nope", map[string]string{"name": "demo"}); err == nil {
		t.Error("an unknown template was accepted")
	}
}

// The built-in templates are public. Firm names belong in a server source.
func TestBuiltinTemplatesContainNoInternalNames(t *testing.T) {
	internal := []string{".hi.fin", "vmhiserver", "hifinab/templates"}
	err := fs.WalkDir(builtinTemplateFiles, "templates", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(builtinTemplateFiles, path)
		if err != nil {
			return err
		}
		for _, name := range internal {
			if strings.Contains(string(data), name) {
				t.Errorf("%s contains the internal name %q", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestBuiltinTemplatesPassTheirCheck generates each template and runs its
// setup and make check. It needs uv, npm, and the network, so it runs only
// with HI_TEMPLATE_CHECK=1.
func TestBuiltinTemplatesPassTheirCheck(t *testing.T) {
	if os.Getenv("HI_TEMPLATE_CHECK") != "1" {
		t.Skip("set HI_TEMPLATE_CHECK=1 to generate the templates and run make check")
	}
	for _, name := range []string{"python", "web", "service", "pipeline", "ml"} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			code, stdout, stderr := runInitIn(t, parent, "", name, "check-"+name, "--yes")
			if code != 0 {
				t.Fatalf("hi init %s: exit %d\n%s%s", name, code, stdout, stderr)
			}
			command := exec.Command("make", "check")
			command.Dir = filepath.Join(parent, "check-"+name)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("make check: %v\n%s", err, output)
			}
		})
	}
}

func TestLockfileGroupMarkersFollowTheName(t *testing.T) {
	lock := string(composeBuiltin(t, "ml", "m").Files["uv.lock"])
	if !strings.Contains(lock, "group-1-m-cpu") || strings.Contains(lock, "group-19-") {
		t.Fatal("uv.lock group markers do not match the project name")
	}
}
