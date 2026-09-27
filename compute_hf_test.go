package main

import (
	"encoding/base64"
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

const fakeHFToken = "hf_test_token_value"

// fakeHF is a minimal Jobs API. Jobs finish with finalStage after their
// first status poll, and their logs are the configured lines.
type fakeHF struct {
	t          *testing.T
	mu         sync.Mutex
	jobs       map[string]map[string]any
	namespaces map[string]string
	created    []map[string]any
	finalStage string
	logLines   []string
	nextID     int
}

func newFakeHF(t *testing.T) *fakeHF {
	t.Helper()
	fake := &fakeHF{
		t:          t,
		jobs:       map[string]map[string]any{},
		namespaces: map[string]string{},
		finalStage: "COMPLETED",
		logLines:   []string{"===== Job started =====", "hello from the job"},
	}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)

	// Colab's fake is set up too so both providers exist, but is signed out
	// unless a test signs it in.
	newFakeColab(t)
	if err := os.Remove(filepath.Join(os.Getenv("HOME"), ".config", "colab-cli", "token.json")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HF_ENDPOINT", server.URL)
	t.Setenv("HF_TOKEN", fakeHFToken)
	t.Setenv("HI_HF_NAMESPACE", "")
	previous := hfPollEvery
	hfPollEvery = time.Millisecond
	t.Cleanup(func() { hfPollEvery = previous })
	return fake
}

func (f *fakeHF) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+fakeHFToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	writeJSON := func(value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	switch {
	case r.URL.Path == "/api/whoami-v2":
		writeJSON(map[string]string{"name": "alice"})
	case r.URL.Path == "/api/jobs/hardware":
		writeJSON([]map[string]any{
			{"name": "cpu-basic", "ram": "16 GB", "unitCostUSD": 0.000167, "unitLabel": "minute"},
			{"name": "a10g-small", "ram": "15 GB", "unitCostUSD": 0.016667, "unitLabel": "minute",
				"accelerator": map[string]string{"type": "gpu", "model": "A10G", "quantity": "1", "vram": "24 GB"}},
			{"name": "rtx-pro-6000", "ram": "180 GB", "unitCostUSD": 0.045833, "unitLabel": "minute",
				"accelerator": map[string]string{"type": "gpu", "model": "RTX PRO 6000", "quantity": "1", "vram": "96 GB"}},
		})
	case len(parts) == 3 && r.Method == http.MethodPost:
		namespace := parts[2]
		if namespace == "broke" {
			w.WriteHeader(http.StatusPaymentRequired)
			writeJSON(map[string]string{"error": "Pre-paid credit balance is insufficient"})
			return
		}
		var spec map[string]any
		_ = json.NewDecoder(r.Body).Decode(&spec)
		f.created = append(f.created, spec)
		f.nextID++
		id := fmt.Sprintf("job%04d", f.nextID)
		status := map[string]any{"stage": "RUNNING", "sshUrl": "ssh://" + id + "@ssh.hf.jobs:2222"}
		if spec["expose"] != nil {
			status["exposeUrls"] = []string{"https://" + id + "--8000.hf.jobs"}
		}
		job := map[string]any{"id": id, "createdAt": time.Now().UTC().Format(time.RFC3339),
			"timeout": spec["timeoutSeconds"], "flavor": spec["flavor"], "labels": spec["labels"],
			"owner": map[string]string{"name": namespace}, "status": status}
		f.jobs[id] = job
		f.namespaces[id] = namespace
		writeJSON(job)
	case len(parts) == 3 && r.Method == http.MethodGet:
		var jobs []map[string]any
		for id, job := range f.jobs {
			if f.namespaces[id] == parts[2] {
				jobs = append(jobs, job)
			}
		}
		writeJSON(jobs)
	case len(parts) == 4 && r.Method == http.MethodGet:
		job, ok := f.jobs[parts[3]]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(job)
		// The next poll sees the job finished.
		job["status"].(map[string]any)["stage"] = f.finalStage
		if f.finalStage == "ERROR" {
			job["status"].(map[string]any)["message"] = "Job failed with exit code: 3. Reason: Error."
		}
	case len(parts) == 5 && parts[4] == "logs":
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range f.logLines {
			data, _ := json.Marshal(map[string]string{"data": line})
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
	case len(parts) == 5 && parts[4] == "cancel":
		f.jobs[parts[3]]["status"].(map[string]any)["stage"] = "CANCELED"
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeHF) lastSpec() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.created) == 0 {
		f.t.Fatal("no job was created")
	}
	return f.created[len(f.created)-1]
}

func (f *fakeHF) createdCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

func requireNoToken(t *testing.T, outputs ...string) {
	t.Helper()
	for _, output := range outputs {
		if strings.Contains(output, fakeHFToken) {
			t.Fatal("the Hugging Face token appeared in output")
		}
	}
}

func TestHFHardwareShowsHourlyPrices(t *testing.T) {
	newFakeHF(t)
	code, stdout, stderr := runComputeTest("hardware", "--on", "hf")
	if code != 0 {
		t.Fatalf("exit code = %d; stderr: %s", code, stderr)
	}
	for _, want := range []string{"cpu-basic", "$0.01/h", "a10g-small", "24 GB VRAM", "$1.00/h", "rtx-pro-6000", "$2.75/h"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("hardware output missing %q:\n%s", want, stdout)
		}
	}
}

func TestHFRunScriptShipsItInTheJobAndStreamsLogs(t *testing.T) {
	fake := newFakeHF(t)
	script := filepath.Join(t.TempDir(), "train.py")
	source := "# /// script\n# dependencies = []\n# ///\nprint('hi')\n"
	writeTestFile(t, script, source, 0o644)
	t.Setenv("WANDB_KEY", "wandb-secret-value")

	code, stdout, stderr := runComputeTest("run", "--on", "hf", "--gpu", "a10g-small", "--name", "train",
		"--max", "90m", "--env", "EPOCHS=3", "--secret", "WANDB_KEY", "--yes", script, "--", "--lr", "0.1")
	if code != 0 {
		t.Fatalf("exit code = %d; stderr: %s\nstdout: %s", code, stderr, stdout)
	}
	spec := fake.lastSpec()
	if spec["dockerImage"] != hfUVImage || spec["flavor"] != "a10g-small" || spec["timeoutSeconds"] != float64(5400) {
		t.Fatalf("unexpected job spec: %v", spec)
	}
	env := spec["environment"].(map[string]any)
	decoded, err := base64.StdEncoding.DecodeString(env["HI_SCRIPT_B64"].(string))
	if err != nil || string(decoded) != source {
		t.Fatalf("the script was not embedded intact: %q", decoded)
	}
	if env["EPOCHS"] != "3" {
		t.Fatalf("environment = %v", env)
	}
	if secrets := spec["secrets"].(map[string]any); secrets["WANDB_KEY"] != "wandb-secret-value" {
		t.Fatalf("secrets = %v", secrets)
	}
	command := fmt.Sprint(spec["command"])
	if !strings.Contains(command, "uv run /tmp/script.py") || !strings.HasSuffix(command, "--lr 0.1]") {
		t.Fatalf("command = %s", command)
	}
	if labels := spec["labels"].(map[string]any); labels["name"] != "train" || labels["managed-by"] != "hi" {
		t.Fatalf("labels = %v", labels)
	}
	if !strings.Contains(stdout, "hello from the job") || strings.Contains(stdout, "===== Job started") {
		t.Fatalf("logs were not streamed cleanly:\n%s", stdout)
	}
	if strings.Contains(stdout+stderr, "wandb-secret-value") {
		t.Fatal("a secret value appeared in output")
	}
	requireNoToken(t, stdout, stderr)
}

func TestHFRunImageAndExitCodes(t *testing.T) {
	fake := newFakeHF(t)
	fake.finalStage = "ERROR"
	code, _, stderr := runComputeTest("run", "--on", "hf", "python:3.12", "--", "python", "-c", "raise SystemExit(1)")
	if code != 3 {
		t.Fatalf("exit code = %d, want the job's own 3; stderr: %s", code, stderr)
	}
	spec := fake.lastSpec()
	if spec["dockerImage"] != "python:3.12" || fmt.Sprint(spec["command"]) != "[python -c raise SystemExit(1)]" {
		t.Fatalf("job spec = %v", spec)
	}
	if spec["flavor"] != "cpu-basic" {
		t.Fatalf("default flavor = %v, want cpu-basic", spec["flavor"])
	}
}

func TestHFRunDetachAndWait(t *testing.T) {
	fake := newFakeHF(t)
	code, stdout, _ := runComputeTest("run", "--on", "hf", "--detach", "--name", "later", "python:3.12", "--", "true")
	if code != 0 || !strings.Contains(stdout, "hi compute logs later --follow") {
		t.Fatalf("exit code = %d, stdout:\n%s", code, stdout)
	}
	code, stdout, _ = runComputeTest("ls")
	if code != 0 || !strings.Contains(stdout, "later") || strings.Contains(stdout, "not started by hi") ||
		!strings.Contains(stdout, "1h") {
		t.Fatalf("a detached hi job should be listed as managed with its limit:\n%s", stdout)
	}
	fake.finalStage = "CANCELED"
	code, stdout, stderr := runComputeTest("wait", "later")
	if code != 130 || !strings.Contains(stdout, "later canceled") {
		t.Fatalf("wait exit code = %d, stdout = %s, stderr = %s", code, stdout, stderr)
	}
	// Finished jobs are still found by name.
	code, stdout, stderr = runComputeTest("status", "later")
	if code != 0 || !strings.Contains(stdout, "State:     canceled") {
		t.Fatalf("status of a finished job: exit code = %d, stdout = %s, stderr = %s", code, stdout, stderr)
	}
	if code, _, stderr = runComputeTest("logs", "later", "-n", "5"); code != 0 {
		t.Fatalf("logs of a finished job failed: %s", stderr)
	}
}

func TestHFPaymentRequiredExplainsCreditsAndNamespace(t *testing.T) {
	newFakeHF(t)
	code, stdout, stderr := runComputeTest("run", "--on", "hf", "--namespace", "broke", "python:3.12", "--", "true")
	if code != 1 || !strings.Contains(stderr, "pre-paid credits") || !strings.Contains(stderr, "--namespace ORG") {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	requireNoToken(t, stdout, stderr)
}

func TestHFDryRunHidesScriptAndSecretsAndSendsNothing(t *testing.T) {
	fake := newFakeHF(t)
	script := filepath.Join(t.TempDir(), "job.py")
	writeTestFile(t, script, "print('secret sauce')\n", 0o644)
	t.Setenv("API_KEY", "value-that-must-not-show")
	code, stdout, stderr := runComputeTest("run", "--on", "hf", "--secret", "API_KEY", "--dry-run", script)
	if code != 0 {
		t.Fatalf("exit code = %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "POST") || !strings.Contains(stdout, "bytes of script") ||
		!strings.Contains(stdout, "<from your environment>") {
		t.Fatalf("dry run output:\n%s", stdout)
	}
	if strings.Contains(stdout, "value-that-must-not-show") || strings.Contains(stdout, base64.StdEncoding.EncodeToString([]byte("print('secret sauce')\n"))) {
		t.Fatal("dry run leaked the secret or the script")
	}
	if fake.createdCount() != 0 {
		t.Fatal("dry run created a job")
	}
	requireNoToken(t, stdout, stderr)
}

func TestHFUpListSSHAndStop(t *testing.T) {
	fake := newFakeHF(t)
	watchers := 0
	startComputeWatcher = func(string) (int, error) { watchers++; return 0, nil }

	code, stdout, stderr := runComputeTest("up", "--gpu", "a10g-small", "--name", "box", "--max", "2h", "--yes")
	if code != 0 {
		t.Fatalf("up: exit code = %d; stderr: %s", code, stderr)
	}
	spec := fake.lastSpec()
	if spec["dockerImage"] != hfGPUImage || fmt.Sprint(spec["command"]) != "[sleep infinity]" ||
		spec["timeoutSeconds"] != float64(7200) || fmt.Sprint(spec["ssh"]) != "map[enabled:true]" {
		t.Fatalf("up job spec = %v", spec)
	}
	if watchers != 0 {
		t.Fatal("a local watcher started although Hugging Face enforces the timeout")
	}
	if !strings.Contains(stdout, "/jobs/alice/job0001") {
		t.Fatalf("up output:\n%s", stdout)
	}

	code, stdout, _ = runComputeTest("ls")
	if code != 0 || !strings.Contains(stdout, "box") || !strings.Contains(stdout, "a10g-small") {
		t.Fatalf("ls: exit code = %d, stdout:\n%s", code, stdout)
	}

	code, _, stderr = runComputeTest("ssh", "box", "--", "nvidia-smi")
	if code != 0 {
		t.Fatalf("ssh: exit code = %d; stderr: %s", code, stderr)
	}
	calls := readTestFile(t, filepath.Join(os.Getenv("FAKE_DIR"), "calls.log"))
	if !strings.Contains(calls, "-p 2222 job0001@ssh.hf.jobs nvidia-smi") {
		t.Fatalf("ssh did not use the job's SSH address:\n%s", calls)
	}

	code, stdout, stderr = runComputeTest("stop", "box")
	if code != 0 || !strings.Contains(stdout, "Stopped box.") {
		t.Fatalf("stop: exit code = %d, stdout = %s, stderr = %s", code, stdout, stderr)
	}
	code, stdout, _ = runComputeTest("ls")
	if code != 0 || strings.Contains(stdout, "box") {
		t.Fatalf("a canceled job is still listed:\n%s", stdout)
	}
}

func TestHFServeRunsTheServerAsTheJob(t *testing.T) {
	fake := newFakeHF(t)
	fake.finalStage = "RUNNING"
	fake.logLines = []string{"hi-state: downloading and loading x", "hi-state: ready"}
	previous := servePollEvery
	servePollEvery = time.Millisecond
	t.Cleanup(func() { servePollEvery = previous })

	code, stdout, stderr := runComputeTest("serve", "--name", "qwen", "--max", "1h", "--yes", "qwen3.8-flash-next")
	if code != 0 {
		t.Fatalf("exit code = %d; stderr: %s\nstdout: %s", code, stderr, stdout)
	}
	spec := fake.lastSpec()
	env := spec["environment"].(map[string]any)
	if spec["dockerImage"] != hfServeImage || spec["flavor"] != "rtx-pro-6000" ||
		env["HOST"] != "0.0.0.0" || env["HI_FOREGROUND"] != "1" || env["QUANT"] != "UD-Q3_K_XL" {
		t.Fatalf("serve job spec = %v", spec)
	}
	if fmt.Sprint(spec["expose"]) != "map[ports:[8000]]" {
		t.Fatalf("expose = %v", spec["expose"])
	}
	decoded, _ := base64.StdEncoding.DecodeString(env["HI_SCRIPT_B64"].(string))
	if string(decoded) != string(serveLlamaCppScript) {
		t.Fatal("the serve script was not embedded intact")
	}
	if !strings.Contains(stdout, "https://job0001--8000.hf.jobs/v1   model: qwen3.8-flash-next") {
		t.Fatalf("serve did not print the API URL:\n%s", stdout)
	}
	requireNoToken(t, stdout, stderr)
}

func TestProviderIsInferredFromHardware(t *testing.T) {
	fake := newFakeHF(t)
	writeTestFile(t, filepath.Join(os.Getenv("HOME"), ".config", "colab-cli", "token.json"), "{}", 0o600)

	if code, _, stderr := runComputeTest("up", "--gpu", "G4", "--name", "c", "--yes"); code != 0 {
		t.Fatalf("G4: %s", stderr)
	}
	if !strings.Contains(readTestFile(t, filepath.Join(os.Getenv("FAKE_DIR"), "calls.log")), "colab new -s c --gpu G4") {
		t.Fatal("G4 did not go to Colab")
	}
	if code, _, stderr := runComputeTest("up", "--gpu", "a10g-small", "--name", "h", "--yes"); code != 0 {
		t.Fatalf("a10g-small: %s", stderr)
	}
	if fake.createdCount() != 1 {
		t.Fatal("a10g-small did not go to Hugging Face")
	}
	if code, _, stderr := runComputeTest("up", "--name", "x", "--yes"); code != 2 || !strings.Contains(stderr, "several providers") {
		t.Fatalf("ambiguous up: exit code = %d, stderr = %s", code, stderr)
	}
}

func TestHFTokenIsReadFromHFHome(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("HF_TOKEN", "")
	t.Setenv("HF_TOKEN_PATH", "")
	t.Setenv("HF_HOME", directory)
	writeTestFile(t, filepath.Join(directory, "token"), "hf_from_file\n", 0o600)
	if got := hfToken(); got != "hf_from_file" {
		t.Fatalf("hfToken() = %q", got)
	}
	t.Setenv("HF_TOKEN", "hf_from_env")
	if got := hfToken(); got != "hf_from_env" {
		t.Fatalf("HF_TOKEN did not take precedence: %q", got)
	}
}
