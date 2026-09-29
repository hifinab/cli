package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const fakeShadeformKey = "sf_test_key"

// fakeShadeform is a minimal Shadeform API. Machines become active with an
// IP on their second info poll.
type fakeShadeform struct {
	t        *testing.T
	mu       sync.Mutex
	machines map[string]map[string]any
	polls    map[string]int
	created  []map[string]any
	deleted  []string
	keys     []map[string]any
	soldOut  map[string]bool
	next     int
}

func newFakeShadeform(t *testing.T) *fakeShadeform {
	t.Helper()
	fake := &fakeShadeform{t: t, machines: map[string]map[string]any{}, polls: map[string]int{}, soldOut: map[string]bool{}}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	newFakeColab(t)
	os.Remove(filepath.Join(os.Getenv("HOME"), ".config", "colab-cli", "token.json"))
	writeTestFile(t, filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519.pub"), "ssh-ed25519 AAAASF me@laptop\n", 0o644)
	t.Setenv("SHADEFORM_API_KEY", fakeShadeformKey)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	previousProviders := computeProviders
	computeProviders = []computeProvider{colabProvider{}, newShadeformProvider()}
	previousBase, previousPoll := shadeformAPIBase, shadeformPollEvery
	shadeformAPIBase, shadeformPollEvery = server.URL+"/v1", time.Millisecond
	t.Cleanup(func() {
		computeProviders = previousProviders
		shadeformAPIBase, shadeformPollEvery = previousBase, previousPoll
	})
	return fake
}

func fakeType(shadeType, cloud string, cents int, vram int, region string, available bool) map[string]any {
	return map[string]any{
		"cloud": cloud, "shade_instance_type": shadeType, "hourly_price": cents, "deployment_type": "vm",
		"configuration": map[string]any{"num_gpus": 1, "gpu_type": shadeType, "vram_per_gpu_in_gb": vram,
			"os_options": []string{"ubuntu22.04", "ubuntu22.04_cuda12.8_shade_os"}},
		"availability": []map[string]any{
			{"region": region, "available": available, "display_name": "Somewhere", "rental_type": "on_demand"},
			{"region": region + "-spot", "available": true, "display_name": "Spot", "rental_type": "spot"},
		},
	}
}

func (f *fakeShadeform) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-KEY") != fakeShadeformKey {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Invalid API key"}`))
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(status int, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(value)
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	switch {
	case path == "/instances/types":
		if r.URL.Query().Get("num_gpus") != "1" {
			reply(400, map[string]any{"message": "the test expects single GPUs"})
			return
		}
		reply(200, map[string]any{"instance_types": []map[string]any{
			fakeType("H100", "hyperstack", 250, 80, "canada-1", !f.soldOut["h100@hyperstack"]),
			fakeType("H100", "lambdalabs", 429, 80, "us-west-1", true),
			fakeType("A4000", "hyperstack", 15, 16, "oslo-1", true),
			fakeType("RTX4090", "excesssupply", 60, 24, "no-1", false),
		}})
	case path == "/sshkeys" && r.Method == http.MethodGet:
		keys := append([]map[string]any{{"id": "managed", "public_key": "ssh-rsa MANAGED shadeform"}}, f.keys...)
		reply(200, map[string]any{"ssh_keys": keys})
	case path == "/sshkeys/add":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		body["id"] = "key-" + body["name"].(string)
		f.keys = append(f.keys, body)
		reply(200, map[string]any{"id": body["id"]})
	case path == "/instances/create":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		key := strings.ToLower(body["shade_instance_type"].(string)) + "@" + body["cloud"].(string)
		if f.soldOut[key] {
			reply(409, map[string]any{"message": "Instance type is not available in this region"})
			return
		}
		f.next++
		id := "sf-" + string(rune('a'+f.next))
		f.created = append(f.created, body)
		machine := map[string]any{"id": id, "name": body["name"], "cloud": body["cloud"], "region": body["region"],
			"shade_instance_type": body["shade_instance_type"], "status": "pending", "ssh_user": "shadeform",
			"ssh_port": 22, "created_at": time.Now().UTC().Format(time.RFC3339), "tags": body["tags"]}
		if autoDelete, ok := body["auto_delete"]; ok {
			machine["auto_delete"] = autoDelete
		}
		f.machines[id] = machine
		reply(200, map[string]any{"id": id})
	case path == "/instances":
		var list []map[string]any
		for _, machine := range f.machines {
			list = append(list, machine)
		}
		reply(200, map[string]any{"instances": list})
	case strings.HasSuffix(path, "/info"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/instances/"), "/info")
		machine, ok := f.machines[id]
		if !ok {
			reply(404, map[string]any{"message": "not found"})
			return
		}
		f.polls[id]++
		if f.polls[id] >= 2 {
			machine["status"], machine["ip"] = "active", "203.0.113.9"
		}
		reply(200, machine)
	case strings.HasSuffix(path, "/delete"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/instances/"), "/delete")
		f.deleted = append(f.deleted, id)
		delete(f.machines, id)
		reply(200, map[string]any{})
	default:
		reply(404, map[string]any{"message": "unexpected " + r.Method + " " + path})
	}
}

func TestShadeformHardwareShowsEachTypeAtItsCheapestFreeOffer(t *testing.T) {
	newFakeShadeform(t)
	code, stdout, stderr := runComputeTest("hardware", "--on", "shadeform")
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, want := range []string{"a4000", "$0.15/h", "hyperstack, Somewhere", "h100", "$2.50/h", "rtx4090", "excesssupply, none free"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("hardware is missing %q:\n%s", want, stdout)
		}
	}
	if strings.Count(stdout, "h100") != 1 || strings.Contains(stdout, "$4.29") {
		t.Fatalf("each type should appear once, at its cheapest offer:\n%s", stdout)
	}
}

func TestShadeformUpRegistersTheKeyAndSetsAutoDelete(t *testing.T) {
	fake := newFakeShadeform(t)
	code, stdout, stderr := runComputeTest("up", "--on", "shadeform", "--gpu", "h100", "--name", "box", "--max", "2h", "--yes")
	if code != 0 || !strings.Contains(stdout, "box is up") {
		t.Fatalf("up: %d\n%s%s", code, stdout, stderr)
	}
	body := fake.created[0]
	if body["cloud"] != "hyperstack" || body["region"] != "canada-1" || body["shade_cloud"] != true ||
		body["os"] != "ubuntu22.04_cuda12.8_shade_os" || body["ssh_key_id"] != "key-hi me@laptop" {
		t.Fatalf("create body: %v", body)
	}
	deadline, err := time.Parse(time.RFC3339, body["auto_delete"].(map[string]any)["date_threshold"].(string))
	if err != nil || deadline.Sub(time.Now()) < 119*time.Minute || deadline.Sub(time.Now()) > 121*time.Minute {
		t.Fatalf("auto_delete %v", body["auto_delete"])
	}
	// A second start reuses the registered key.
	runComputeTest("up", "--on", "shadeform", "--gpu", "a4000", "--name", "box2", "--max", "1h", "--yes")
	if len(fake.keys) != 1 {
		t.Fatalf("the key was registered %d times", len(fake.keys))
	}
}

func TestShadeformPinsACloudAndListsSSHAndStops(t *testing.T) {
	fake := newFakeShadeform(t)
	if code, _, stderr := runComputeTest("up", "--on", "shadeform", "--gpu", "h100@lambdalabs", "--name", "pinned", "--max", "1h", "--yes"); code != 0 {
		t.Fatal(stderr)
	}
	if fake.created[0]["cloud"] != "lambdalabs" {
		t.Fatalf("created on %v", fake.created[0]["cloud"])
	}
	code, stdout, _ := runComputeTest("ls")
	if code != 0 || !strings.Contains(stdout, "pinned") || !strings.Contains(stdout, "h100@lambdalabs") {
		t.Fatalf("ls:\n%s", stdout)
	}
	provider := newShadeformProvider()
	target, err := provider.ssh("pinned")
	if err != nil || target.destination != "shadeform@203.0.113.9" {
		t.Fatalf("ssh target %+v %v", target, err)
	}
	if code, _, stderr := runComputeTest("stop", "pinned"); code != 0 || len(fake.deleted) != 1 {
		t.Fatalf("stop: %d %s", code, stderr)
	}
}

func TestShadeformSoldOutIsANoCapacityErrorWithAlternatives(t *testing.T) {
	fake := newFakeShadeform(t)
	provider := newShadeformProvider()
	hardware, err := provider.lookupHardware("h100@hyperstack")
	if err != nil {
		t.Fatal(err)
	}
	fake.soldOut["h100@hyperstack"] = true
	err = provider.create(upRequest{name: "x", hardware: hardware, max: time.Hour}, &strings.Builder{}, &strings.Builder{})
	var soldOut noCapacityError
	if err == nil || !errorsAs(err, &soldOut) {
		t.Fatalf("want a no-capacity error, got %v", err)
	}
	alternatives, err := provider.alternatives("h100@hyperstack", 5.00)
	if err != nil || len(alternatives) != 1 || alternatives[0].name != "h100@lambdalabs" {
		t.Fatalf("alternatives %+v %v", alternatives, err)
	}
	if none, _ := provider.alternatives("h100@hyperstack", 4.00); len(none) != 0 {
		t.Fatalf("an alternative above the ceiling: %+v", none)
	}
}

func TestShadeformRejectsImagesAndRuns(t *testing.T) {
	newFakeShadeform(t)
	if code, _, stderr := runComputeTest("up", "--on", "shadeform", "--gpu", "a4000", "--image", "x", "--max", "1h", "--yes"); code == 0 || !strings.Contains(stderr, "virtual machines") {
		t.Fatalf("--image: %d %s", code, stderr)
	}
	script := filepath.Join(t.TempDir(), "train.py")
	writeTestFile(t, script, "print(1)\n", 0o644)
	if code, _, stderr := runComputeTest("run", "--on", "shadeform", "--gpu", "a4000", "--yes", script); code == 0 || !strings.Contains(stderr, "no run-to-completion") {
		t.Fatalf("run: %d %s", code, stderr)
	}
}

func TestShadeformSignInChecksAndSavesTheKey(t *testing.T) {
	newFakeShadeform(t)
	t.Setenv("SHADEFORM_API_KEY", "")
	provider := newShadeformProvider()
	if _, err := provider.signIn("wrong"); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("a wrong key: %v", err)
	}
	path, err := provider.signIn(fakeShadeformKey)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 || shadeformToken() != fakeShadeformKey {
		t.Fatalf("saved key: mode %v", info.Mode().Perm())
	}
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }
