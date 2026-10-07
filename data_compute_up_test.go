package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// upWithData connects a device to an exposed data server that manages
// RunPod (approving starts by policy), with Shadeform on the device's own
// key, and an ssh that records what it was sent.
func upWithData(t *testing.T) (*testServer, *fakeShadeform, string) {
	t.Helper()
	shadeform := newFakeShadeform(t)
	fakeDir, path, providers := os.Getenv("FAKE_DIR"), os.Getenv("PATH"), computeProviders
	ts, _, public := newExposedDataServer(t)
	t.Setenv("FAKE_DIR", fakeDir)
	t.Setenv("PATH", path)
	computeProviders = providers
	writeTestFile(t, filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519.pub"), "ssh-ed25519 AAAASF me@laptop\n", 0o644)
	writeTestFile(t, filepath.Join(fakeDir, "bin", "ssh"),
		"#!/bin/sh\nprintf 'ssh %s\\n' \"$*\" >> \"$FAKE_DIR/calls.log\"\ncat >> \"$FAKE_DIR/stdin.log\"\n", 0o755)
	writeTestFile(t, policyPath(ts.dir), `{"groups":{"students":{"auto_approve":{"max_price_per_hour":1,"max_hours":2}}}}`, 0o600)
	previous := startComputeWatcher
	startComputeWatcher = func(string) (int, error) { return 0, nil }
	t.Cleanup(func() { startComputeWatcher = previous })
	return ts, shadeform, public
}

// dataUpSent is everything the machine was sent on stdin.
func dataUpSent(t *testing.T) string {
	t.Helper()
	return readTestFile(t, filepath.Join(os.Getenv("FAKE_DIR"), "stdin.log"))
}

func TestComputeUpDataHandsTheMachineARunToken(t *testing.T) {
	ts, _, public := upWithData(t)
	// RunPod through the hi server, approved by policy.
	code, stdout, stderr := runComputeTest("up", "--on", "runpod", "--gpu", "rtx-4090", "--name", "box", "--max", "2h",
		"--data", "hifinab/ranker", "--data", "dataset:hifinab/bars", "--reason", "train", "--yes")
	if code != 0 {
		t.Fatalf("exit code %d\n%s%s", code, stdout, stderr)
	}
	sent := dataUpSent(t)
	if !strings.Contains(sent, "export HF_ENDPOINT='"+public+"/hf'") || !strings.Contains(sent, "export HF_TOKEN="+dataTokenPrefix) ||
		!strings.Contains(sent, "export HI_DATA_REPOS='hifinab/ranker hifinab/bars'") {
		t.Fatalf("the machine got:\n%s", sent)
	}
	calls := readTestFile(t, filepath.Join(os.Getenv("FAKE_DIR"), "calls.log"))
	if !strings.Contains(calls, "root@10.0.0.9") || !strings.Contains(calls, "cat > ~/.hi/data.env") || !strings.Contains(calls, "~/.bashrc ~/.profile") {
		t.Fatalf("ssh calls:\n%s", calls)
	}
	if !strings.Contains(stdout, "box can read hifinab/ranker, hifinab/bars through the hi server until") {
		t.Fatalf("stdout:\n%s", stdout)
	}
	// The token is in neither the pod's settings nor the output.
	token := sent[strings.Index(sent, "HF_TOKEN=")+len("HF_TOKEN="):]
	token = token[:strings.Index(token, "\n")]
	if strings.Contains(stdout+stderr+calls, token) {
		t.Fatal("the run token leaked into the output or the ssh command line")
	}
	audit, _ := os.ReadFile(filepath.Join(ts.dir, "audit.jsonl"))
	if !strings.Contains(string(audit), `"action":"run token"`) || !strings.Contains(string(audit), "run box") {
		t.Fatalf("audit = %s", audit)
	}

	// Shadeform with the device's own key works the same way.
	code, stdout, stderr = runComputeTest("up", "--on", "shadeform", "--gpu", "h100", "--name", "vm", "--max", "1h", "--data", "hifinab/ranker", "--yes")
	if code != 0 || !strings.Contains(stdout, "vm can read hifinab/ranker") || strings.Count(dataUpSent(t), "export HF_TOKEN=") != 2 {
		t.Fatalf("shadeform: %d\n%s%s", code, stdout, stderr)
	}
}

func TestComputeUpDataRefusesBeforeStarting(t *testing.T) {
	_, shadeform, _ := upWithData(t)
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"--on", "colab", "--gpu", "t4"}, "RunPod and Shadeform"},
		{[]string{"--on", "shadeform", "--gpu", "h100", "--data", "hifinab/bars/data"}, "whole repositories"},
		{[]string{"--on", "shadeform", "--gpu", "h100", "--data", "hifinab/secret"}, "not among what you may download"},
		{[]string{"--on", "runpod", "--gpu", "rtx-4090", "--no-wait"}, "--no-wait"},
	} {
		args := append([]string{"up", "--name", "x", "--max", "1h", "--reason", "train", "--data", "hifinab/ranker", "--yes"}, test.args...)
		code, stdout, stderr := runComputeTest(args...)
		if code == 0 || !strings.Contains(stderr, test.want) {
			t.Errorf("%v: code %d, want %q\n%s%s", test.args, code, test.want, stdout, stderr)
		}
	}
	if calls, _ := os.ReadFile(filepath.Join(os.Getenv("FAKE_DIR"), "calls.log")); strings.Contains(string(calls), "ssh") {
		t.Fatalf("a refused start reached a machine:\n%s", calls)
	}
	if len(shadeform.created) != 0 {
		t.Fatalf("a refused start created a machine: %v", shadeform.created)
	}
}

func TestComputeUpDataKeepsTokensOffCommunityCloud(t *testing.T) {
	request := upRequest{hardware: computeHardware{name: "rtx-a4000@community"}}
	if _, err := checkDataUp(&runpodProvider{}, request, []string{"hifinab/ranker"}); err == nil || !strings.Contains(err.Error(), "Community Cloud") {
		t.Fatalf("err = %v", err)
	}
	request = upRequest{hardware: computeHardware{name: "rtx-4090"}, noWait: true}
	if _, err := checkDataUp(&runpodProvider{}, request, []string{"hifinab/ranker"}); err == nil || !strings.Contains(err.Error(), "--no-wait") {
		t.Fatalf("err = %v", err)
	}
}
