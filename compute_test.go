package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeColab installs fake `colab` and `ssh` binaries that append their
// arguments to calls.log and replay canned output.
type fakeColab struct {
	t         *testing.T
	directory string
}

const fakeColabScript = `#!/bin/sh
printf 'colab %s\n' "$*" >> "$FAKE_DIR/calls.log"
case "$1" in
sessions) cat "$FAKE_DIR/sessions" 2>/dev/null || echo "[colab] No active sessions found on server." ;;
usage) printf 'Current balance: 1234.50 compute units\nUsage rate: 8.90/hr\nActive assignments: 1\n' ;;
new)
	name="$3"; hardware="CPU"; variant="DEFAULT"
	[ "$4" = "--gpu" ] && hardware="$5" && variant="GPU"
	printf '[%s] ep-%s | Hardware: %s | Shape: Standard | Variant: %s\n' "$name" "$name" "$hardware" "$variant" >> "$FAKE_DIR/sessions"
	echo "[colab] Session READY." ;;
stop)
	if grep -q "^\[$3\]" "$FAKE_DIR/sessions" 2>/dev/null; then
		grep -v "^\[$3\]" "$FAKE_DIR/sessions" > "$FAKE_DIR/sessions.new"; mv "$FAKE_DIR/sessions.new" "$FAKE_DIR/sessions"
		echo "[colab] Session terminated."
	else
		echo "[colab] Session '$3' not found."
	fi ;;
run) echo "script output"; exit "${FAKE_RUN_EXIT:-0}" ;;
esac
`

func newFakeColab(t *testing.T) *fakeColab {
	t.Helper()
	requireLinux(t)
	directory := t.TempDir()
	home := filepath.Join(directory, "home")
	for _, path := range []string{
		filepath.Join(home, ".config", "colab-cli"),
		filepath.Join(home, ".ssh"),
		filepath.Join(directory, "bin"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(home, ".config", "colab-cli", "token.json"), "{}", 0o600)
	writeTestFile(t, filepath.Join(home, ".ssh", "id_ed25519"), "key", 0o600)
	writeTestFile(t, filepath.Join(directory, "bin", "colab"), fakeColabScript, 0o755)
	writeTestFile(t, filepath.Join(directory, "bin", "ssh"),
		"#!/bin/sh\nprintf 'ssh %s\\n' \"$*\" >> \"$FAKE_DIR/calls.log\"\n", 0o755)

	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(directory, "state"))
	t.Setenv("FAKE_DIR", directory)
	t.Setenv("PATH", filepath.Join(directory, "bin")+":/usr/bin:/bin")

	previous := startComputeWatcher
	startComputeWatcher = func(string) (int, error) { return 0, nil }
	t.Cleanup(func() { startComputeWatcher = previous })
	return &fakeColab{t: t, directory: directory}
}

func (f *fakeColab) calls() string {
	data, err := os.ReadFile(filepath.Join(f.directory, "calls.log"))
	if err != nil {
		return ""
	}
	return string(data)
}

func (f *fakeColab) setSessions(lines ...string) {
	writeTestFile(f.t, filepath.Join(f.directory, "sessions"), strings.Join(lines, "\n")+"\n", 0o600)
}

func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func runComputeTest(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"compute"}, args...), strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestComputeUpRejectsUnknownHardwareBeforeCallingColab(t *testing.T) {
	fake := newFakeColab(t)
	code, _, stderr := runComputeTest("up", "--gpu", "B200", "--yes")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, `unknown colab hardware "B200"`) || !strings.Contains(stderr, "G4") {
		t.Fatalf("error did not name the hardware and the choices: %s", stderr)
	}
	if strings.Contains(fake.calls(), "colab new") {
		t.Fatal("colab new ran for unknown hardware")
	}
}

func TestComputeUpStartsColabSessionAndRecordsDeadline(t *testing.T) {
	fake := newFakeColab(t)
	code, stdout, stderr := runComputeTest("up", "--gpu", "g4", "--name", "qwen", "--max", "2h", "--yes")
	if code != 0 {
		t.Fatalf("exit code = %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(fake.calls(), "colab new -s qwen --gpu G4\n") {
		t.Fatalf("colab new was not called with the normalized GPU:\n%s", fake.calls())
	}
	if !strings.Contains(stdout, "hi compute tunnel qwen 8000") {
		t.Fatalf("next steps missing from output:\n%s", stdout)
	}
	records, err := loadComputeRecords()
	if err != nil {
		t.Fatal(err)
	}
	record, ok := records["qwen"]
	if !ok || record.Provider != "colab" || record.Hardware != "G4" {
		t.Fatalf("record = %+v", records)
	}
	if lifetime := record.Deadline.Sub(record.Created); lifetime < 119*time.Minute || lifetime > 121*time.Minute {
		t.Fatalf("recorded lifetime = %s, want 2h", lifetime)
	}
}

func TestComputeUpNeedsYesWithoutTerminalForPaidHardware(t *testing.T) {
	fake := newFakeColab(t)
	code, _, stderr := runComputeTest("up", "--gpu", "T4", "--name", "t4")
	if code != 1 || !strings.Contains(stderr, "--yes") {
		t.Fatalf("exit code = %d, stderr = %s; want a --yes requirement", code, stderr)
	}
	if strings.Contains(fake.calls(), "colab new") {
		t.Fatal("colab new ran without confirmation")
	}
}

func TestComputeUpDryRunStartsNothing(t *testing.T) {
	fake := newFakeColab(t)
	code, stdout, stderr := runComputeTest("up", "--gpu", "A100", "--high-mem", "--name", "big", "--dry-run")
	if code != 0 {
		t.Fatalf("exit code = %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Would run: colab new -s big --gpu A100 --high-mem") {
		t.Fatalf("dry run did not show the command:\n%s", stdout)
	}
	if fake.calls() != "" {
		t.Fatalf("dry run called colab:\n%s", fake.calls())
	}
}

func TestComputeUpRejectsLifetimeBeyondColabLimit(t *testing.T) {
	newFakeColab(t)
	code, _, stderr := runComputeTest("up", "--max", "30h", "--yes")
	if code != 2 || !strings.Contains(stderr, "limit of 24h") {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
}

func TestComputeListParsesSessionsAndMarksExternalOnes(t *testing.T) {
	fake := newFakeColab(t)
	fake.setSessions(
		"[colab] Syncing...",
		"[mine] ep1 | Hardware: G4 | Shape: Standard | Variant: GPU",
		"[other] ep2 | Hardware: CPU | Shape: High-RAM | Variant: DEFAULT",
		"[?] ep3 | Hardware: T4 | Shape: Standard | Variant: GPU",
	)
	now := time.Now()
	if err := saveComputeRecord(computeRecord{
		Name: "mine", Provider: "colab", Hardware: "G4", Created: now.Add(-time.Hour), Deadline: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runComputeTest("ls")
	if code != 0 {
		t.Fatalf("exit code = %d; stderr: %s", code, stderr)
	}
	for _, want := range []string{"mine", "G4", "other (not started by hi)", "cpu High-RAM", "? (not started by hi)", "Colab balance: 1234.50 compute units"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("ls output missing %q:\n%s", want, stdout)
		}
	}
}

func TestComputeListStopsInstancesPastTheirDeadline(t *testing.T) {
	fake := newFakeColab(t)
	fake.setSessions("[old] ep1 | Hardware: T4 | Shape: Standard | Variant: GPU")
	past := time.Now().Add(-time.Minute)
	if err := saveComputeRecord(computeRecord{
		Name: "old", Provider: "colab", Hardware: "T4", Created: past.Add(-time.Hour), Deadline: past,
	}); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runComputeTest("ls")
	if code != 0 {
		t.Fatalf("exit code = %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(fake.calls(), "colab stop -s old") {
		t.Fatalf("expired instance was not stopped:\n%s", fake.calls())
	}
	if !strings.Contains(stdout, "No instances are running.") {
		t.Fatalf("stopped instance still listed:\n%s", stdout)
	}
	if records, _ := loadComputeRecords(); len(records) != 0 {
		t.Fatalf("record kept after stop: %+v", records)
	}
}

func TestComputeListForgetsRecordsOfVanishedInstances(t *testing.T) {
	newFakeColab(t)
	now := time.Now()
	if err := saveComputeRecord(computeRecord{
		Name: "gone", Provider: "colab", Hardware: "T4", Created: now, Deadline: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runComputeTest("ls"); code != 0 {
		t.Fatalf("ls failed: %s", stderr)
	}
	if records, _ := loadComputeRecords(); len(records) != 0 {
		t.Fatalf("record of a vanished instance was kept: %+v", records)
	}
}

func TestComputeStopReportsUnknownSession(t *testing.T) {
	newFakeColab(t)
	code, _, stderr := runComputeTest("stop", "colab/nope")
	if code != 1 || !strings.Contains(stderr, `no session named "nope"`) {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
}

func TestComputeStopAllNeedsYesWithoutTerminal(t *testing.T) {
	fake := newFakeColab(t)
	fake.setSessions(
		"[a] ep1 | Hardware: T4 | Shape: Standard | Variant: GPU",
		"[b] ep2 | Hardware: G4 | Shape: Standard | Variant: GPU",
	)
	if code, _, _ := runComputeTest("stop", "--all"); code != 1 {
		t.Fatalf("stop --all without --yes returned %d, want 1", code)
	}
	if strings.Contains(fake.calls(), "colab stop") {
		t.Fatal("sessions were stopped without confirmation")
	}
	code, _, stderr := runComputeTest("stop", "--all", "--yes")
	if code != 0 {
		t.Fatalf("stop --all --yes failed: %s", stderr)
	}
	calls := fake.calls()
	if !strings.Contains(calls, "colab stop -s a") || !strings.Contains(calls, "colab stop -s b") {
		t.Fatalf("not every session was stopped:\n%s", calls)
	}
}

func TestComputeSSHUsesColabProxy(t *testing.T) {
	fake := newFakeColab(t)
	fake.setSessions("[qwen] ep1 | Hardware: G4 | Shape: Standard | Variant: GPU")
	code, _, stderr := runComputeTest("ssh", "qwen", "--", "nvidia-smi")
	if code != 0 {
		t.Fatalf("exit code = %d; stderr: %s", code, stderr)
	}
	calls := fake.calls()
	if !strings.Contains(calls, "ProxyCommand="+filepath.Join(fake.directory, "bin", "colab")+" ssh --proxy-mode -s qwen") {
		t.Fatalf("ssh did not use the colab proxy:\n%s", calls)
	}
	if !strings.Contains(calls, "root@colab nvidia-smi") {
		t.Fatalf("ssh did not run the remote command:\n%s", calls)
	}
}

func TestComputeTunnelBindsLocalhostAndRejectsReservedPort(t *testing.T) {
	fake := newFakeColab(t)
	fake.setSessions("[qwen] ep1 | Hardware: G4 | Shape: Standard | Variant: GPU")

	code, _, stderr := runComputeTest("tunnel", "qwen", "8080")
	if code != 1 || !strings.Contains(stderr, "Colab's own proxy") {
		t.Fatalf("reserved port: exit code = %d, stderr = %s", code, stderr)
	}

	code, _, stderr = runComputeTest("tunnel", "qwen", "8000:9000")
	if code != 0 {
		t.Fatalf("exit code = %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(fake.calls(), "-L 127.0.0.1:9000:127.0.0.1:8000 root@colab") {
		t.Fatalf("tunnel did not forward to localhost only:\n%s", fake.calls())
	}
}

func TestComputeRunPassesTimeoutEnvAndExitCode(t *testing.T) {
	fake := newFakeColab(t)
	script := filepath.Join(fake.directory, "train.py")
	writeTestFile(t, script, "print(1)\n", 0o644)
	t.Setenv("FAKE_RUN_EXIT", "3")

	code, stdout, stderr := runComputeTest("run", "--gpu", "T4", "--max", "90m", "--env", "A=1", "--yes", script, "--", "--epochs", "2")
	if code != 3 {
		t.Fatalf("exit code = %d, want the script's 3; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "script output") {
		t.Fatalf("script output was not streamed:\n%s", stdout)
	}
	want := "colab run --gpu T4 --timeout 5400 --env A=1 " + script + " --epochs 2\n"
	if !strings.Contains(fake.calls(), want) {
		t.Fatalf("colab run call = %q, want %q", fake.calls(), want)
	}
}

func TestComputeRunRejectsSecretsOnColab(t *testing.T) {
	fake := newFakeColab(t)
	script := filepath.Join(fake.directory, "train.py")
	writeTestFile(t, script, "print(1)\n", 0o644)
	t.Setenv("HF_TOKEN", "hf_secret_value")
	code, stdout, stderr := runComputeTest("run", "--secret", "HF_TOKEN", "--yes", script)
	if code != 2 || !strings.Contains(stderr, "no secret store") {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if strings.Contains(stdout+stderr+fake.calls(), "hf_secret_value") {
		t.Fatal("secret value leaked")
	}
}

func TestComputeProvidersExplainsMissingSignIn(t *testing.T) {
	newFakeColab(t)
	if err := os.Remove(filepath.Join(os.Getenv("HOME"), ".config", "colab-cli", "token.json")); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ := runComputeTest("providers")
	if code != 0 || !strings.Contains(stdout, "colab    not signed in") || !strings.Contains(stdout, "colab usage") {
		t.Fatalf("exit code = %d, output:\n%s", code, stdout)
	}
}

func TestComputeMenuStartsInstanceFromAnswers(t *testing.T) {
	fake := newFakeColab(t)
	// Start, hardware 1 (cpu), name "play", 1h lifetime, no high-RAM, then quit.
	answers := strings.NewReader("1\n1\nplay\n1h\nn\n8\n")
	var stdout, stderr bytes.Buffer
	if err := computeMenu(answers, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fake.calls(), "colab new -s play\n") {
		t.Fatalf("menu did not start the instance:\ncalls:\n%s\nstdout:\n%s\nstderr:\n%s", fake.calls(), stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "$ hi compute up --on colab --gpu cpu --name play --max 1h") {
		t.Fatalf("menu did not show the equivalent command:\n%s", stdout.String())
	}
}

func TestFormatDuration(t *testing.T) {
	for duration, want := range map[time.Duration]string{
		4 * time.Hour:                   "4h",
		90 * time.Minute:                "1h30m",
		5 * time.Minute:                 "5m",
		-time.Minute:                    "0s",
		5 * time.Second:                 "5s",
		2*time.Hour + 29*time.Second:    "2h",
		59*time.Minute + 45*time.Second: "1h",
	} {
		if got := formatDuration(duration); got != want {
			t.Errorf("formatDuration(%s) = %q, want %q", duration, got, want)
		}
	}
}

// fakeRemoteSSH replaces the fake ssh with one that plays a remote instance
// for `serve`: it stores the uploaded script and reports the given state.
func (f *fakeColab) fakeRemoteSSH(state string) {
	writeTestFile(f.t, filepath.Join(f.directory, "bin", "ssh"), `#!/bin/sh
printf 'ssh %s\n' "$*" >> "$FAKE_DIR/calls.log"
for last in "$@"; do :; done
case "$last" in
*"cat > ~/.hi/serve-llama-cpp.sh"*) cat > "$FAKE_DIR/uploaded.sh" ;;
*"cat ~/.hi/state"*) printf '%s\n' "$FAKE_STATE" ;;
esac
exit 0
`, 0o755)
	f.t.Setenv("FAKE_STATE", state)
}

func TestComputeServeRecipeStartsUploadsPollsAndTunnels(t *testing.T) {
	fake := newFakeColab(t)
	fake.fakeRemoteSSH("ready")
	previous := servePollEvery
	servePollEvery = time.Millisecond
	t.Cleanup(func() { servePollEvery = previous })

	code, stdout, stderr := runComputeTest("serve", "--name", "qwen", "--yes", "qwen3.8-flash-next")
	if code != 0 {
		t.Fatalf("exit code = %d; stderr: %s\nstdout: %s", code, stderr, stdout)
	}
	calls := fake.calls()
	if !strings.Contains(calls, "colab new -s qwen --gpu G4\n") {
		t.Fatalf("serve did not start a G4 for the recipe:\n%s", calls)
	}
	uploaded := readTestFile(t, filepath.Join(fake.directory, "uploaded.sh"))
	if uploaded != string(serveLlamaCppScript) {
		t.Fatal("the uploaded script differs from the embedded serve script")
	}
	for _, want := range []string{
		"REPO=unsloth/Qwen3.8-Flash-Next-GGUF",
		"QUANT=UD-Q3_K_XL",
		"CTX=131072",
		`EXTRA_ARGS='-ot per_layer_token_embd\.weight=CPU --lazy-mode off`,
		"setsid nohup bash ~/.hi/serve-llama-cpp.sh",
		"-L 127.0.0.1:8080:127.0.0.1:8000 root@colab",
	} {
		if !strings.Contains(calls, want) {
			t.Fatalf("calls missing %q:\n%s", want, calls)
		}
	}
	if !strings.Contains(stdout, "http://127.0.0.1:8080/v1   model: qwen3.8-flash-next") {
		t.Fatalf("serve did not print the API URL:\n%s", stdout)
	}
}

func TestComputeServeReportsRemoteFailure(t *testing.T) {
	fake := newFakeColab(t)
	fake.fakeRemoteSSH("failed: download (see logs)")
	previous := servePollEvery
	servePollEvery = time.Millisecond
	t.Cleanup(func() { servePollEvery = previous })

	code, _, stderr := runComputeTest("serve", "--gpu", "T4", "--name", "small", "--quant", "Q4_K_M", "--yes", "unsloth/Qwen3-8B-GGUF")
	if code != 1 || !strings.Contains(stderr, "failed: download") || !strings.Contains(stderr, "hi compute logs small") {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr)
	}
	if strings.Contains(fake.calls(), "-L 127.0.0.1") {
		t.Fatal("a tunnel was opened for a failed server")
	}
}

func TestComputeServeValidatesModelAndHardware(t *testing.T) {
	fake := newFakeColab(t)
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"serve", "--gpu", "T4", "not-a-repo"}, "neither a recipe"},
		{[]string{"serve", "--gpu", "T4", "owner/Model-GGUF"}, "--quant"},
		{[]string{"serve", "--gpu", "cpu", "--quant", "Q4_K_M", "owner/Model-GGUF"}, "serving needs a GPU"},
		{[]string{"serve", "--gpu", "T4", "--quant", "Q4 K", "owner/Model-GGUF"}, "invalid characters"},
	} {
		code, _, stderr := runComputeTest(test.args...)
		if code != 2 || !strings.Contains(stderr, test.want) {
			t.Errorf("%v: exit code = %d, stderr = %s; want %q", test.args, code, stderr, test.want)
		}
	}
	if strings.Contains(fake.calls(), "colab new") {
		t.Fatal("an instance started despite invalid input")
	}
}
