package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestPlanFromMenu(t *testing.T) {
	installed := map[string]bool{"uv": true, "claude": true, "docker": true}
	tests := []struct {
		name    string
		checked []string
		want    installPlan
	}{
		{
			name:    "keeps installed tools and installs new ones",
			checked: []string{"uv", "claude", "docker", "gh"},
			want:    installPlan{install: []string{"gh"}},
		},
		{
			name:    "removes unchecked installed tools",
			checked: []string{"uv"},
			want:    installPlan{remove: []string{"docker", "claude"}},
		},
		{
			name:    "update reinstalls every checked tool",
			checked: []string{"uv", "claude", "docker", "upgrade"},
			want:    installPlan{install: []string{"uv", "docker", "claude"}, upgrade: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := planFromMenu(test.checked, installed)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("plan = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestPlanFromMenuNeedsUVForColab(t *testing.T) {
	_, err := planFromMenu([]string{"colab"}, map[string]bool{"uv": true})
	if err == nil || !strings.Contains(err.Error(), "Colab CLI needs uv") {
		t.Fatalf("err = %v, want the uv dependency error", err)
	}
	// An installed Colab CLI keeps working without uv.
	plan, err := planFromMenu([]string{"colab"}, map[string]bool{"uv": true, "colab": true})
	if err != nil || !reflect.DeepEqual(plan.remove, []string{"uv"}) {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
}

func TestOrderedIDs(t *testing.T) {
	got, err := orderedIDs([]string{"hf", "claude", "hf", "terminal"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"terminal", "claude", "hf"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	if _, err := orderedIDs([]string{"upgrade"}, false); err == nil {
		t.Fatal("upgrade should not be removable")
	}
	if _, err := orderedIDs([]string{"vim"}, true); err == nil {
		t.Fatal("unknown tools should be rejected")
	}
}

func TestInstallWithoutTerminalNeedsTools(t *testing.T) {
	restore := itemInstalled
	itemInstalled = func(installItem) bool { return false }
	defer func() { itemInstalled = restore }()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"install"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "needs a terminal") || !strings.Contains(stderr.String(), "claude") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestInstallList(t *testing.T) {
	restore := itemInstalled
	itemInstalled = func(item installItem) bool { return item.id == "gh" }
	defer func() { itemInstalled = restore }()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"install", "--list"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "gh" && !strings.Contains(line, "installed") {
			t.Fatalf("gh line = %q, want installed", line)
		}
		if len(fields) > 0 && fields[0] == "claude" && strings.Contains(line, "installed") {
			t.Fatalf("claude line = %q, want not installed", line)
		}
	}
}

func TestInstallRejectsUnknownTool(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"uninstall", "vim"}, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), `unknown tool "vim"`) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestSetupScriptHandlesEveryTool(t *testing.T) {
	script := string(setupScript)
	for _, item := range installItems {
		if item.action {
			continue
		}
		if !strings.Contains(script, "installing "+item.id) {
			t.Errorf("setup script never installs %s", item.id)
		}
		if !strings.Contains(script, "\n    "+item.id+")") {
			t.Errorf("setup script cannot remove %s", item.id)
		}
	}
}
