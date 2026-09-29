package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeManaged is a provider the test server manages in place of RunPod.
type fakeManaged struct {
	mu        sync.Mutex
	instances map[string]upRequest
	stopped   []string
	soldOut   map[string]bool
}

func newFakeManaged() *fakeManaged {
	return &fakeManaged{instances: map[string]upRequest{}, soldOut: map[string]bool{}}
}

var fakeHardware = []computeHardware{
	{name: "l4", kind: "GPU", memory: "24 GB", rate: "$0.49/h", paid: true},
	{name: "rtx-3090", kind: "GPU", memory: "24 GB", rate: "$0.50/h", paid: true},
	{name: "rtx-4090", kind: "GPU", memory: "24 GB", rate: "$0.74/h", paid: true},
	{name: "a100", kind: "GPU", memory: "80 GB", rate: "$1.99/h", paid: true},
}

func fakeHourly(hardware computeHardware) float64 {
	var hourly float64
	fmt.Sscanf(hardware.rate, "$%f/h", &hourly)
	return hourly
}

func (f *fakeManaged) alternatives(name string, factor float64) ([]computeHardware, error) {
	var wanted computeHardware
	for _, hardware := range fakeHardware {
		if hardware.name == name {
			wanted = hardware
		}
	}
	var result []computeHardware
	for _, hardware := range fakeHardware {
		if hardware.name != name && !f.soldOut[hardware.name] && fakeHourly(hardware) <= fakeHourly(wanted)*factor {
			result = append(result, hardware)
		}
	}
	return result, nil
}

func (*fakeManaged) name() string                  { return "runpod" }
func (*fakeManaged) check() providerStatus         { return providerStatus{installed: true, signedIn: true} }
func (*fakeManaged) maxLifetime() time.Duration    { return 0 }
func (*fakeManaged) enforcesLifetime() bool        { return false }
func (*fakeManaged) reservedPorts() map[int]string { return nil }
func (*fakeManaged) account() (string, error)      { return "", nil }
func (*fakeManaged) validateRun(runRequest) error  { return nil }
func (*fakeManaged) upCommand(upRequest) []string  { return nil }
func (*fakeManaged) runCommand(runRequest) []string {
	return nil
}
func (*fakeManaged) runJob(runRequest, io.Reader, io.Writer, io.Writer) (int, error) { return 0, nil }
func (*fakeManaged) wait(string, io.Writer, io.Writer) (int, error)                  { return 0, nil }
func (*fakeManaged) logs(string, bool, int, io.Reader, io.Writer, io.Writer) error   { return nil }

func (*fakeManaged) hardware() ([]computeHardware, error) { return fakeHardware, nil }

func (f *fakeManaged) create(request upRequest, stdout, _ io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.soldOut[request.hardware.name] {
		return noCapacityError{fmt.Errorf("none of %s is free", request.hardware.name)}
	}
	f.instances[request.name] = request
	io.WriteString(stdout, "Created pod abc; waiting for it to start\n")
	return nil
}

func (f *fakeManaged) list() ([]computeInstance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var instances []computeInstance
	for name, request := range f.instances {
		instances = append(instances, computeInstance{name: name, hardware: request.hardware.name, state: "running"})
	}
	return instances, nil
}

func (f *fakeManaged) stop(name string, _, _ io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.instances, name)
	f.stopped = append(f.stopped, name)
	return nil
}

func (*fakeManaged) ssh(string) (sshTarget, error) {
	return sshTarget{options: []string{"-p", "2222"}, destination: "root@10.0.0.9"}, nil
}

func (f *fakeManaged) started(name string) (upRequest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	request, ok := f.instances[name]
	return request, ok
}

type testServer struct {
	server *hiServer
	fake   *fakeManaged
	url    string
	dir    string
}

// newTestServer runs a hi server over HTTP with a fake RunPod, and gives
// the test a device home that has never connected.
func newTestServer(t *testing.T) *testServer {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "server")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	server, err := openServer(dir, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeManaged()
	server.providers["runpod"] = fake
	http := httptest.NewServer(server.clientHandler())
	t.Cleanup(http.Close)

	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, ".ssh", "id_ed25519.pub"), "ssh-ed25519 AAAAtest alice@laptop\n", 0o600)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("HI_COMPUTE_PROVIDER", "")
	t.Setenv("RUNPOD_API_KEY", "")
	t.Setenv("HF_TOKEN", "")
	t.Setenv("HF_HOME", filepath.Join(root, "hf"))
	t.Setenv("PATH", "/usr/bin:/bin")

	previousProviders := computeProviders
	computeProviders = []computeProvider{colabProvider{}, newHFProvider(), newRunpodProvider()}
	previousConnect, previousManaged := connectPollEvery, managedPollEvery
	connectPollEvery, managedPollEvery = 10*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() {
		computeProviders = previousProviders
		connectPollEvery, managedPollEvery = previousConnect, previousManaged
	})
	return &testServer{server: server, fake: fake, url: http.URL, dir: dir}
}

func runHi(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func (ts *testServer) pending(t *testing.T, kind string) string {
	t.Helper()
	ts.server.mu.Lock()
	defer ts.server.mu.Unlock()
	for id, request := range ts.server.state.Requests {
		if request.Kind == kind && request.State == "pending" {
			return id
		}
	}
	t.Fatalf("no pending %s request", kind)
	return ""
}

// connectAs enrolls this test device and has bob approve it.
func (ts *testServer) connectAs(t *testing.T, user, group string) {
	t.Helper()
	code, stdout, stderr := runHi("connect", ts.url, "--user", user, "--no-wait")
	if code != exitPending || !strings.Contains(stdout, "hi server approve e-") {
		t.Fatalf("connect: code %d\n%s%s", code, stdout, stderr)
	}
	if _, err := ts.server.decide(ts.pending(t, "enroll"), "bob", true, group, ""); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runHi("connect", "status")
	if code != 0 || !strings.Contains(stdout, "as "+user+" ("+group+")") || !strings.Contains(stdout, "Managed by the server: runpod") {
		t.Fatalf("connect status: code %d\n%s%s", code, stdout, stderr)
	}
}

func TestManagedComputeGoesThroughApproval(t *testing.T) {
	ts := newTestServer(t)
	ts.connectAs(t, "alice", "students")

	code, stdout, _ := runHi("compute", "providers")
	if code != 0 || !strings.Contains(stdout, "runpod   managed by "+ts.url) || !strings.Contains(stdout, "colab") {
		t.Fatalf("providers did not label runpod as managed:\n%s", stdout)
	}

	code, _, stderr := runHi("compute", "up", "--on", "runpod", "--gpu", "rtx-4090", "--name", "job1", "--max", "1h", "--yes", "--no-wait")
	if code != 2 || !strings.Contains(stderr, "--reason") {
		t.Fatalf("a request without a reason: code %d, stderr %s", code, stderr)
	}
	code, stdout, stderr = runHi("compute", "up", "--on", "runpod", "--gpu", "rtx-4090", "--name", "job1",
		"--max", "none", "--reason", "thesis", "--yes", "--no-wait")
	if code == 0 || !strings.Contains(stderr, "time limit") {
		t.Fatalf("a request without a time limit: code %d, stderr %s", code, stderr)
	}

	code, stdout, stderr = runHi("compute", "up", "--on", "runpod", "--gpu", "rtx-4090", "--name", "job1",
		"--max", "1h", "--reason", "thesis LoRA sweep", "--yes", "--no-wait")
	if code != exitPending || !strings.Contains(stdout, "Sent request r-") || !strings.Contains(stderr, "--wait") {
		t.Fatalf("up --no-wait: code %d\n%s%s", code, stdout, stderr)
	}
	if _, ok := ts.fake.started("job1"); ok {
		t.Fatal("the instance started before anyone approved it")
	}
	id := ts.pending(t, "compute")

	if _, err := ts.server.decide(id, "alice", true, "", ""); err == nil || !strings.Contains(err.Error(), "own request") {
		t.Fatalf("alice approved her own request: %v", err)
	}
	if _, err := ts.server.decide(id, "bob", true, "", ""); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runHi("compute", "requests", id, "--wait", "--timeout", "5s")
	if code != 0 || !strings.Contains(stdout, "job1 is running") || !strings.Contains(stdout, "hi compute ssh job1") {
		t.Fatalf("requests --wait: code %d\n%s%s", code, stdout, stderr)
	}
	started, ok := ts.fake.started("job1")
	if !ok || !started.brokered || started.publicKey != "ssh-ed25519 AAAAtest alice@laptop" || started.max != time.Hour {
		t.Fatalf("the server started it wrongly: %+v", started)
	}

	code, stdout, _ = runHi("compute", "ls")
	if code != 0 || !strings.Contains(stdout, "job1") || !strings.Contains(stdout, "managed by") {
		t.Fatalf("ls: code %d\n%s", code, stdout)
	}

	code, stdout, stderr = runHi("compute", "stop", "job1")
	if code != 0 || !strings.Contains(stdout, "Stopped job1") {
		t.Fatalf("stop: code %d\n%s%s", code, stdout, stderr)
	}
	ts.server.mu.Lock()
	request := *ts.server.state.Requests[id]
	_, leased := ts.server.state.Leases["job1"]
	ts.server.mu.Unlock()
	if leased || request.State != "stopped" || request.StoppedBy != "alice" {
		t.Fatalf("after stop: leased %v, request %+v", leased, request)
	}
}

func TestManagedComputeDenialExitsWithStatus4(t *testing.T) {
	ts := newTestServer(t)
	ts.connectAs(t, "alice", "students")
	code, _, stderr := runHi("compute", "up", "--on", "runpod", "--gpu", "rtx-4090", "--name", "big",
		"--max", "8h", "--reason", "just because", "--yes", "--no-wait")
	if code != exitPending {
		t.Fatalf("up: code %d, stderr %s", code, stderr)
	}
	id := ts.pending(t, "compute")
	if _, err := ts.server.decide(id, "bob", false, "", "use a smaller GPU"); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runHi("compute", "requests", id, "--wait")
	if code != exitDenied || !strings.Contains(stderr, "denied by bob: use a smaller GPU") {
		t.Fatalf("requests --wait on a denial: code %d, stderr %s", code, stderr)
	}
	code, stdout, _ := runHi("compute", "requests", id, "--json")
	if code != exitDenied || !strings.Contains(stdout, `"deny_reason": "use a smaller GPU"`) || strings.Contains(stdout, "public_key") {
		t.Fatalf("requests --json: code %d\n%s", code, stdout)
	}
	if _, ok := ts.fake.started("big"); ok {
		t.Fatal("a denied request started")
	}
}

func TestManagedProviderFailsClosedWhenServerIsDown(t *testing.T) {
	ts := newTestServer(t)
	ts.connectAs(t, "alice", "staff")
	calls := 0
	runpod := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer runpod.Close()
	previous := runpodAPIBase
	runpodAPIBase = runpod.URL
	defer func() { runpodAPIBase = previous }()
	t.Setenv("RUNPOD_API_KEY", "a-personal-key")

	connection, _ := loadServerConnection()
	connection.URL = "http://127.0.0.1:1"
	if err := saveServerConnection(*connection); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runHi("compute", "up", "--on", "runpod", "--gpu", "rtx-4090", "--max", "1h", "--reason", "x", "--yes")
	if code == 0 || !strings.Contains(stderr, "can't reach the hi server") {
		t.Fatalf("up with the server down: code %d, stderr %s", code, stderr)
	}
	if calls != 0 {
		t.Fatalf("hi fell back to the personal RunPod key (%d calls)", calls)
	}
}

func TestNeverConnectedAndColabAreNotManaged(t *testing.T) {
	newTestServer(t)
	restore, err := withManagedProviders()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range computeProviders {
		if isManaged(provider) {
			t.Fatalf("%s is managed on a device that never connected", provider.name())
		}
	}
	restore()
	code, _, stderr := runHi("compute", "up", "--on", "runpod", "--max", "1h", "--no-wait", "--yes")
	if code != 2 || !strings.Contains(stderr, "--no-wait applies only") {
		t.Fatalf("--no-wait without a server: code %d, stderr %s", code, stderr)
	}

	if err := saveServerConnection(serverConnection{URL: "http://127.0.0.1:1", User: "alice",
		Providers: []apiProvider{{Name: "colab"}, {Name: "runpod"}}}); err != nil {
		t.Fatal(err)
	}
	restore, err = withManagedProviders()
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	for _, provider := range computeProviders {
		if provider.name() == "colab" && isManaged(provider) {
			t.Fatal("colab became managed")
		}
		if provider.name() == "runpod" && !isManaged(provider) {
			t.Fatal("runpod is not managed")
		}
	}
}

func TestDisconnectRestoresOwnKeys(t *testing.T) {
	ts := newTestServer(t)
	ts.connectAs(t, "alice", "staff")
	code, stdout, _ := runHi("disconnect")
	if code != 0 || !strings.Contains(stdout, "own keys again") {
		t.Fatalf("disconnect: code %d\n%s", code, stdout)
	}
	for _, path := range []string{serverConnectionPath(), deviceKeyPath()} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s is still there", path)
		}
	}
	code, stdout, _ = runHi("compute", "providers")
	if code != 0 || strings.Contains(stdout, "managed") {
		t.Fatalf("providers after disconnect:\n%s", stdout)
	}
}

func TestServerRejectsForgedReplayedAndStaleRequests(t *testing.T) {
	ts := newTestServer(t)
	ts.connectAs(t, "alice", "staff")
	key, err := loadDeviceKey(false)
	if err != nil {
		t.Fatal(err)
	}
	send := func(request *http.Request) (int, string) {
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(data)
	}

	request, _ := http.NewRequest(http.MethodGet, ts.url+"/v1/me", nil)
	signRequest(request, key, nil)
	if status, body := send(request); status != http.StatusOK {
		t.Fatalf("a signed request failed: %d %s", status, body)
	}
	replay, _ := http.NewRequest(http.MethodGet, ts.url+"/v1/me", nil)
	replay.Header = request.Header.Clone()
	if status, body := send(replay); status != http.StatusUnauthorized || !strings.Contains(body, "replayed") {
		t.Fatalf("a replayed request was accepted: %d %s", status, body)
	}

	body := `{"provider":"runpod","hardware":"rtx-4090","name":"x","max_seconds":60,"public_key":"k","reason":"r"}`
	tampered, _ := http.NewRequest(http.MethodPost, ts.url+"/v1/requests", strings.NewReader(strings.Replace(body, "60", "86400", 1)))
	signRequest(tampered, key, []byte(body))
	if status, reply := send(tampered); status != http.StatusUnauthorized || !strings.Contains(reply, "bad signature") {
		t.Fatalf("a tampered request was accepted: %d %s", status, reply)
	}

	stale, _ := http.NewRequest(http.MethodGet, ts.url+"/v1/me", nil)
	signRequest(stale, key, nil)
	old := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	stale.Header.Set(headerTime, old)
	if status, reply := send(stale); status != http.StatusUnauthorized {
		t.Fatalf("a stale request was accepted: %d %s", status, reply)
	}

	_, stranger, _ := ed25519.GenerateKey(rand.Reader)
	unknown, _ := http.NewRequest(http.MethodGet, ts.url+"/v1/me", nil)
	signRequest(unknown, stranger, nil)
	if status, reply := send(unknown); status != http.StatusForbidden || !strings.Contains(reply, "not enrolled") {
		t.Fatalf("an unknown device was accepted: %d %s", status, reply)
	}
}

func TestReconcileStopsAtTheLimitAndReportsUnleasedInstances(t *testing.T) {
	ts := newTestServer(t)
	now := time.Now()
	ts.fake.instances["late"] = upRequest{name: "late"}
	ts.fake.instances["fine"] = upRequest{name: "fine"}
	ts.fake.instances["rogue"] = upRequest{name: "rogue"}
	ts.server.state.Leases["late"] = &serverLease{Name: "late", Provider: "runpod", User: "alice",
		Started: now.Add(-2 * time.Hour), Deadline: now.Add(-time.Minute)}
	ts.server.state.Leases["fine"] = &serverLease{Name: "fine", Provider: "runpod", User: "alice",
		Started: now.Add(-time.Hour), Deadline: now.Add(time.Hour)}
	ts.server.state.Leases["gone"] = &serverLease{Name: "gone", Provider: "runpod", User: "alice",
		Started: now.Add(-time.Hour), Deadline: now.Add(time.Hour)}
	ts.server.state.Requests["r-old"] = &serverRequest{ID: "r-old", Kind: "compute", State: "pending",
		Created: now.Add(-time.Hour)}

	ts.server.reconcile()

	if len(ts.fake.stopped) != 1 || ts.fake.stopped[0] != "late" {
		t.Fatalf("stopped %v, want only late", ts.fake.stopped)
	}
	for name, want := range map[string]bool{"late": false, "gone": false, "fine": true} {
		if _, ok := ts.server.state.Leases[name]; ok != want {
			t.Fatalf("lease %s present = %v, want %v", name, ok, want)
		}
	}
	if state := ts.server.state.Requests["r-old"].State; state != "expired" {
		t.Fatalf("an old pending request is %s, want expired", state)
	}
	audit, _ := os.ReadFile(filepath.Join(ts.dir, "audit.jsonl"))
	if !strings.Contains(string(audit), `"unleased instance","subject":"runpod/rogue"`) ||
		!strings.Contains(string(audit), `"actor":"limit","action":"stopped","subject":"late"`) {
		t.Fatalf("audit log:\n%s", audit)
	}
	ts.server.reconcile()
	audit2, _ := os.ReadFile(filepath.Join(ts.dir, "audit.jsonl"))
	if strings.Count(string(audit2), "runpod/rogue") != 1 {
		t.Fatal("the same unleased instance was reported twice")
	}
}

func TestServerAdminCommandsUseTheSocket(t *testing.T) {
	ts := newTestServer(t)
	socket := filepath.Join(ts.dir, "admin.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("no Unix socket here: %v", err)
	}
	admin := &http.Server{Handler: ts.server.adminHandler()}
	go admin.Serve(listener)
	defer admin.Close()

	code, stdout, stderr := runHi("connect", ts.url, "--user", "carol", "--no-wait")
	if code != exitPending {
		t.Fatalf("connect: %d %s%s", code, stdout, stderr)
	}
	code, stdout, _ = runHi("server", "requests", "--dir", ts.dir)
	if code != 0 || !strings.Contains(stdout, "carol") || !strings.Contains(stdout, "join from") {
		t.Fatalf("server requests:\n%s", stdout)
	}
	id := ts.pending(t, "enroll")
	code, _, stderr = runHi("server", "approve", id, "--dir", ts.dir, "--as", "dana")
	if code == 0 || !strings.Contains(stderr, "--group") {
		t.Fatalf("approving a new user without a group: %d %s", code, stderr)
	}
	code, stdout, stderr = runHi("server", "approve", id, "--group", "staff", "--dir", ts.dir, "--as", "dana")
	if code != 0 || !strings.Contains(stdout, "Approved carol's device") {
		t.Fatalf("server approve: %d %s%s", code, stdout, stderr)
	}
	if code, stdout, _ = runHi("connect", "status"); code != 0 {
		t.Fatalf("connect status after approval: %d\n%s", code, stdout)
	}

	runHi("compute", "up", "--on", "runpod", "--gpu", "rtx-4090", "--name", "carol-job",
		"--max", "2h", "--reason", "eval", "--yes", "--no-wait")
	request := ts.pending(t, "compute")
	if code, stdout, stderr = runHi("server", "approve", request, "--dir", ts.dir, "--as", "dana"); code != 0 {
		t.Fatalf("approve compute: %d %s%s", code, stdout, stderr)
	}
	if code, stdout, stderr = runHi("compute", "requests", request, "--wait", "--timeout", "5s"); code != 0 {
		t.Fatalf("wait: %d %s%s", code, stdout, stderr)
	}
	code, stdout, _ = runHi("server", "ls", "--dir", ts.dir)
	if code != 0 || !strings.Contains(stdout, "carol-job") || !strings.Contains(stdout, "carol") {
		t.Fatalf("server ls:\n%s", stdout)
	}
	code, stdout, stderr = runHi("server", "stop", "--user", "carol", "--dir", ts.dir, "--as", "dana")
	if code != 0 || !strings.Contains(stdout, "Stopped carol-job") {
		t.Fatalf("server stop --user: %d %s%s", code, stdout, stderr)
	}
	code, stdout, _ = runHi("server", "audit", "--dir", ts.dir)
	for _, want := range []string{"dana approved enrollment", "dana approved " + request, "dana stopped carol-job"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("audit is missing %q:\n%s", want, stdout)
		}
	}
	code, stdout, _ = runHi("server", "user", "list", "--dir", ts.dir)
	if code != 0 || !strings.Contains(stdout, "carol") || !strings.Contains(stdout, "staff") {
		t.Fatalf("user list:\n%s", stdout)
	}
}

func TestConnectKeyPreApprovesADevice(t *testing.T) {
	ts := newTestServer(t)
	code, key, _ := runHi("connect", "key")
	if code != 0 {
		t.Fatal("connect key failed")
	}
	ts.server.state.Users["erin"] = &serverUser{Name: "erin", Group: "agents"}
	loaded, err := loadDeviceKey(false)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := keyFingerprint(loaded.Public().(ed25519.PublicKey))
	if devicePublicKey(loaded) != strings.TrimSpace(key) {
		t.Fatal("connect key printed another key")
	}
	ts.server.state.Devices[fingerprint] = &serverDevice{Fingerprint: fingerprint, User: "erin"}
	code, stdout, stderr := runHi("connect", ts.url, "--user", "erin")
	if code != 0 || !strings.Contains(stdout, "Connected to "+ts.url+" as erin (agents)") {
		t.Fatalf("a pre-approved device had to wait: %d\n%s%s", code, stdout, stderr)
	}
}

func TestRequestsCannotReuseANameOnTheProviderAccount(t *testing.T) {
	ts := newTestServer(t)
	ts.connectAs(t, "alice", "staff")
	ts.fake.instances["taken"] = upRequest{name: "taken"}
	code, _, stderr := runHi("compute", "up", "--on", "runpod", "--gpu", "rtx-4090", "--name", "taken",
		"--max", "1h", "--reason", "x", "--yes", "--no-wait")
	if code == 0 || code == exitPending || !strings.Contains(stderr, "already exists") {
		t.Fatalf("a request reused a pod's name: code %d, stderr %s", code, stderr)
	}
}

func TestAStartInterruptedByARestartFails(t *testing.T) {
	ts := newTestServer(t)
	ts.server.state.Requests["r-mid"] = &serverRequest{ID: "r-mid", Kind: "compute", State: "starting", Name: "mid"}
	ts.server.mu.Lock()
	ts.server.saveLocked()
	ts.server.mu.Unlock()
	reopened, err := openServer(ts.dir, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if request := reopened.state.Requests["r-mid"]; request.State != "failed" || !strings.Contains(request.Error, "restarted") {
		t.Fatalf("after a restart: %+v", request)
	}
}

func TestMaskedInputEchoesStarsAndHandlesEditing(t *testing.T) {
	var echo bytes.Buffer
	// Typed "abc", Backspace, an arrow key, a bracketed paste of "de", Enter.
	got, err := readMasked(strings.NewReader("abc\x7f\x1b[D\x1b[200~de\x1b[201~\rignored"), &echo)
	if err != nil || string(got) != "abde" {
		t.Fatalf("got %q, %v", got, err)
	}
	if echo.String() != "***\b \b**" {
		t.Fatalf("echo %q", echo.String())
	}
	if _, err := readMasked(strings.NewReader("ab\x03"), &echo); err == nil {
		t.Fatal("Ctrl-C did not cancel")
	}
}

func TestProviderAddWorksBeforeTheServerRuns(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "server")
	previous := serverKeyCheck
	serverKeyCheck = map[string]func(string) error{"runpod": func(key string) error { return nil }}
	defer func() { serverKeyCheck = previous }()
	t.Setenv("RUNPOD_API_KEY", "")

	if code, _, stderr := runHi("server", "init", "--listen", "127.0.0.1:7474", "--dir", dir); code != 0 {
		t.Fatalf("init: %s", stderr)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"server", "provider", "add", "runpod", "--dir", dir}, strings.NewReader("rpa_secret\n"), &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "Saved the runpod key") || strings.Contains(stdout.String(), "rpa_secret") {
		t.Fatalf("provider add: %d\n%s%s", code, stdout.String(), stderr.String())
	}
	server, err := openServer(dir, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if server.keys["runpod"] != "rpa_secret" || server.providers["runpod"] == nil {
		t.Fatalf("the key was not stored: %v", server.keys)
	}
	info, _ := os.Stat(filepath.Join(dir, "keys.json"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("keys.json mode %v", info.Mode().Perm())
	}
	code = run([]string{"server", "provider", "add", "runpd", "--dir", dir}, strings.NewReader("x\n"), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("a misspelled provider was accepted: %d", code)
	}
}

func TestSoldOutHardwareIsReplacedWithinTheApprovedPrice(t *testing.T) {
	ts := newTestServer(t)
	ts.connectAs(t, "alice", "staff")
	ts.fake.soldOut["l4"] = true
	runHi("compute", "up", "--on", "runpod", "--gpu", "l4", "--name", "swap", "--max", "1h", "--reason", "x", "--yes", "--no-wait")
	id := ts.pending(t, "compute")
	if _, err := ts.server.decide(id, "bob", true, "", ""); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runHi("compute", "requests", id, "--wait", "--timeout", "5s")
	if code != 0 || !strings.Contains(stdout, "swap is running on rtx-3090 ($0.50/h); the approved l4 ($0.49/h) was sold out") {
		t.Fatalf("replacement: code %d\n%s%s", code, stdout, stderr)
	}
	if started, ok := ts.fake.started("swap"); !ok || started.hardware.name != "rtx-3090" {
		t.Fatalf("started %+v", started)
	}
	if lease := ts.server.state.Leases["swap"]; lease.Hardware != "rtx-3090" || lease.Rate != "$0.50/h" {
		t.Fatalf("lease %+v", lease)
	}
}

func TestSoldOutHardwareFailsWhenNothingFitsThePriceBound(t *testing.T) {
	ts := newTestServer(t)
	ts.connectAs(t, "alice", "staff")
	ts.server.fallbackFactor = 1.01
	ts.fake.soldOut["l4"] = true
	runHi("compute", "up", "--on", "runpod", "--gpu", "l4", "--name", "nope", "--max", "1h", "--reason", "x", "--yes", "--no-wait")
	id := ts.pending(t, "compute")
	if _, err := ts.server.decide(id, "bob", true, "", ""); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runHi("compute", "requests", id, "--wait", "--timeout", "5s")
	if code != 1 || !strings.Contains(stderr, "costs at most 1.01x") {
		t.Fatalf("code %d, stderr %s", code, stderr)
	}
	if _, ok := ts.fake.started("nope"); ok {
		t.Fatal("a replacement above the price bound started")
	}
}

func TestAuditEntriesStayOnOneLine(t *testing.T) {
	ts := newTestServer(t)
	ts.server.audit("server", "failed to start", "r-1", "first line\nsecond line")
	data, _ := os.ReadFile(filepath.Join(ts.dir, "audit.jsonl"))
	if !strings.Contains(string(data), `"detail":"first line · second line"`) {
		t.Fatalf("audit: %s", data)
	}
}
