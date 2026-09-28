package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const fakeRunpodKey = "rpa_test_key_value"

// fakeRunpod is a minimal RunPod REST API v2. Pods become RUNNING with an SSH
// address on their second status poll.
type fakeRunpod struct {
	t       *testing.T
	mu      sync.Mutex
	pods    map[string]map[string]any
	polls   map[string]int
	created []map[string]any
	deleted []string
	next    int
	failure int
}

func newFakeRunpod(t *testing.T) *fakeRunpod {
	t.Helper()
	fake := &fakeRunpod{t: t, pods: map[string]map[string]any{}, polls: map[string]int{}}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)

	newFakeColab(t)
	if err := os.Remove(filepath.Join(os.Getenv("HOME"), ".config", "colab-cli", "token.json")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519.pub"), "ssh-ed25519 AAAATEST me@laptop\n", 0o644)
	t.Setenv("RUNPOD_API_KEY", fakeRunpodKey)
	previousBase, previousPoll := runpodAPIBase, runpodPollEvery
	runpodAPIBase, runpodPollEvery = server.URL+"/v2", time.Millisecond
	t.Cleanup(func() { runpodAPIBase, runpodPollEvery = previousBase, previousPoll })
	return fake
}

func (f *fakeRunpod) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+fakeRunpodKey {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	writeJSON := func(status int, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
	}
	path := strings.TrimPrefix(r.URL.Path, "/v2")
	switch {
	case path == "/catalog/gpus":
		writeJSON(200, map[string]any{"gpus": []map[string]any{
			{"id": "NVIDIA GeForce RTX 4090", "name": "RTX 4090", "memory": 24, "secure": true, "price": map[string]any{"secure": 0.69}},
			{"id": "NVIDIA A100 80GB PCIe", "name": "A100 PCIe", "memory": 80, "secure": true, "price": map[string]any{"secure": 1.64}},
			{"id": "NVIDIA RTX A2000", "name": "RTX A2000", "memory": 6, "secure": false, "price": map[string]any{"secure": 0}},
		}})
	case path == "/catalog/cpus":
		writeJSON(200, map[string]any{"cpus": []map[string]any{
			{"id": "cpu3c", "name": "Compute", "ramGbPerVcpu": 2, "price": map[string]any{"securePerVcpu": 0.03}},
		}})
	case path == "/pods" && r.Method == http.MethodPost:
		if f.failure != 0 {
			writeJSON(f.failure, map[string]any{"title": "Refused", "detail": "no capacity for that GPU"})
			return
		}
		var spec map[string]any
		_ = json.NewDecoder(r.Body).Decode(&spec)
		f.created = append(f.created, spec)
		f.next++
		id := fmt.Sprintf("pod%03d", f.next)
		pod := map[string]any{"id": id, "name": spec["name"], "status": "PROVISIONING", "env": spec["env"],
			"createdAt": time.Now().UTC().Format(time.RFC3339), "gpu": spec["gpu"], "cpu": spec["cpu"],
			"ssh": map[string]any{"direct": nil}}
		f.pods[id] = pod
		writeJSON(201, pod)
	case path == "/pods" && r.Method == http.MethodGet:
		var pods []map[string]any
		for _, pod := range f.pods {
			pods = append(pods, pod)
		}
		writeJSON(200, map[string]any{"pods": pods, "pagination": map[string]any{"hasNextPage": false}})
	case strings.HasPrefix(path, "/pods/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(path, "/pods/")
		pod, ok := f.pods[id]
		if !ok {
			writeJSON(404, map[string]any{"title": "Not found"})
			return
		}
		f.polls[id]++
		if f.polls[id] >= 2 {
			pod["status"] = "RUNNING"
			pod["ssh"] = map[string]any{"direct": map[string]any{"host": "203.0.113.7", "port": 40022, "username": "root"}}
		}
		writeJSON(200, pod)
	case strings.HasPrefix(path, "/pods/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(path, "/pods/")
		f.deleted = append(f.deleted, id)
		if pod, ok := f.pods[id]; ok {
			pod["status"] = "TERMINATED"
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(404, map[string]any{"title": "Not found", "detail": path})
	}
}

func (f *fakeRunpod) lastSpec() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.created) == 0 {
		f.t.Fatal("no pod was created")
	}
	return f.created[len(f.created)-1]
}

func TestRunpodHardwareHasShortNamesAndPrices(t *testing.T) {
	newFakeRunpod(t)
	code, stdout, stderr := runComputeTest("hardware", "--on", "runpod")
	if code != 0 {
		t.Fatalf("exit code = %d; stderr: %s", code, stderr)
	}
	for _, want := range []string{"cpu-cpu3c", "2 vCPU, 4 GB RAM", "$0.06/h", "rtx-4090", "24 GB VRAM", "$0.69/h", "a100-pcie", "$1.64/h"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("hardware output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "a2000") {
		t.Fatalf("a GPU without secure-cloud capacity was listed:\n%s", stdout)
	}
	if strings.Index(stdout, "cpu-cpu3c") > strings.Index(stdout, "rtx-4090") {
		t.Fatal("CPU options should come first")
	}
}

func TestRunpodUpCreatesASSHPodWaitsAndInstallsTheWatchdog(t *testing.T) {
	fake := newFakeRunpod(t)
	watchers := 0
	startComputeWatcher = func(string) (int, error) { watchers++; return 0, nil }

	code, stdout, stderr := runComputeTest("up", "--gpu", "rtx-4090", "--name", "box", "--max", "2", "--yes")
	if code != 0 {
		t.Fatalf("exit code = %d; stderr: %s\nstdout: %s", code, stderr, stdout)
	}
	spec := fake.lastSpec()
	gpu := spec["gpu"].(map[string]any)
	env := spec["env"].(map[string]any)
	if spec["name"] != "box" || gpu["id"] != "NVIDIA GeForce RTX 4090" || spec["image"] != runpodImage ||
		fmt.Sprint(spec["ports"]) != "[22/tcp]" || spec["startSsh"] != true || spec["cloud"] != "SECURE" {
		t.Fatalf("pod spec = %v", spec)
	}
	if env["PUBLIC_KEY"] != "ssh-ed25519 AAAATEST me@laptop" || env["HI_MANAGED"] != "1" {
		t.Fatalf("env = %v", env)
	}
	if watchers != 1 {
		t.Fatalf("local watchers = %d, want 1 (RunPod has no native limit)", watchers)
	}
	calls := readTestFile(t, filepath.Join(os.Getenv("FAKE_DIR"), "calls.log"))
	if !strings.Contains(calls, "-p 40022") || !strings.Contains(calls, "root@203.0.113.7") ||
		!strings.Contains(calls, "~/.hi/watchdog.sh") {
		t.Fatalf("the watchdog was not installed over SSH:\n%s", calls)
	}
	if !strings.Contains(stdout, "running") {
		t.Fatalf("no progress while waiting:\n%s", stdout)
	}
	if strings.Contains(stdout+stderr+calls, fakeRunpodKey) {
		t.Fatal("the RunPod API key leaked")
	}
}

func TestRunpodListSSHAndStopTerminates(t *testing.T) {
	fake := newFakeRunpod(t)
	if code, _, stderr := runComputeTest("up", "--on", "runpod", "--gpu", "a100-pcie", "--name", "big", "--max", "1", "--yes"); code != 0 {
		t.Fatalf("up failed: %s", stderr)
	}
	code, stdout, _ := runComputeTest("ls")
	if code != 0 || !strings.Contains(stdout, "big") || !strings.Contains(stdout, "a100-pcie") || strings.Contains(stdout, "not started by hi") {
		t.Fatalf("ls:\n%s", stdout)
	}
	if code, _, stderr := runComputeTest("ssh", "big", "--", "nvidia-smi"); code != 0 {
		t.Fatalf("ssh failed: %s", stderr)
	}
	if calls := readTestFile(t, filepath.Join(os.Getenv("FAKE_DIR"), "calls.log")); !strings.Contains(calls, "root@203.0.113.7 nvidia-smi") {
		t.Fatalf("ssh calls:\n%s", calls)
	}
	code, stdout, stderr := runComputeTest("stop", "big")
	if code != 0 || !strings.Contains(stdout, "Stopped big.") || len(fake.deleted) != 1 {
		t.Fatalf("stop: exit code = %d, stdout = %s, stderr = %s, deleted = %v", code, stdout, stderr, fake.deleted)
	}
	if _, stdout, _ = runComputeTest("ls"); strings.Contains(stdout, "big") {
		t.Fatalf("a terminated pod is still listed:\n%s", stdout)
	}
}

func TestRunpodErrorsExplainBalanceAndCapacity(t *testing.T) {
	fake := newFakeRunpod(t)
	fake.failure = http.StatusPaymentRequired
	code, _, stderr := runComputeTest("up", "--gpu", "rtx-4090", "--name", "poor", "--yes")
	if code != 1 || !strings.Contains(stderr, "balance") || !strings.Contains(stderr, "console.runpod.io/user/billing") {
		t.Fatalf("402: exit code = %d, stderr = %s", code, stderr)
	}
	fake.failure = http.StatusBadRequest
	code, _, stderr = runComputeTest("up", "--gpu", "rtx-4090", "--name", "busy", "--yes")
	if code != 1 || !strings.Contains(stderr, "no capacity") || !strings.Contains(stderr, "try another --gpu") {
		t.Fatalf("400: exit code = %d, stderr = %s", code, stderr)
	}
}

func TestRunpodRejectsRunsAndOtherProvidersFlags(t *testing.T) {
	newFakeRunpod(t)
	script := filepath.Join(t.TempDir(), "job.py")
	writeTestFile(t, script, "print(1)\n", 0o644)
	if code, _, stderr := runComputeTest("run", "--on", "runpod", "--yes", script); code != 2 || !strings.Contains(stderr, "hi compute up --on runpod") {
		t.Fatalf("run: exit code = %d, stderr = %s", code, stderr)
	}
	if code, _, stderr := runComputeTest("up", "--on", "runpod", "--gpu", "rtx-4090", "--high-mem", "--yes"); code != 2 || !strings.Contains(stderr, "Colab only") {
		t.Fatalf("--high-mem: exit code = %d, stderr = %s", code, stderr)
	}
	if code, _, stderr := runComputeTest("up", "--on", "runpod", "--gpu", "rtx-4090", "--namespace", "x", "--yes"); code != 2 || !strings.Contains(stderr, "Hugging Face only") {
		t.Fatalf("--namespace: exit code = %d, stderr = %s", code, stderr)
	}
}

func TestRunpodTokenFromEnvironmentOrRunpodctlConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RUNPOD_API_KEY", "")
	if got := runpodToken(); got != "" {
		t.Fatalf("token without configuration = %q", got)
	}
	if err := os.MkdirAll(filepath.Join(home, ".runpod"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, ".runpod", "config.toml"), "apiUrl = \"https://api.runpod.io/graphql\"\napiKey = \"rpa_from_file\"\n", 0o600)
	if got := runpodToken(); got != "rpa_from_file" {
		t.Fatalf("token from config.toml = %q", got)
	}
	t.Setenv("RUNPOD_API_KEY", "rpa_from_env")
	if got := runpodToken(); got != "rpa_from_env" {
		t.Fatalf("RUNPOD_API_KEY did not win: %q", got)
	}
}

func TestRunpodSlug(t *testing.T) {
	for input, want := range map[string]string{
		"NVIDIA GeForce RTX 4090": "rtx-4090", "RTX 4090": "rtx-4090", "A100 PCIe": "a100-pcie",
		"NVIDIA H100 80GB HBM3": "h100-80gb-hbm3", "AMD Instinct MI300X OAM": "instinct-mi300x-oam",
	} {
		if got := runpodSlug(input); got != want {
			t.Errorf("runpodSlug(%q) = %q, want %q", input, got, want)
		}
	}
}
