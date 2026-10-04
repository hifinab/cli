package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeNetbirdExpose stands in for `netbird expose <port> …`: it prints a
// name and a URL straight to the relay's port, and records its arguments.
func fakeNetbirdExpose(t *testing.T, output string) string {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "args")
	script := filepath.Join(dir, "netbird")
	body := output
	if body == "" {
		body = "echo 'Service exposed successfully!'\necho '  Name:     hi-test-ab12'\necho \"  URL:      http://127.0.0.1:$2\"\nexec sleep 600\n"
	}
	writeTestFile(t, script, "#!/bin/sh\necho \"$*\" > "+record+"\n"+body, 0o755)
	previous, previousAddress := exposeCommand, netExposeAddress
	exposeCommand = script
	netExposeAddress = func() (string, error) { return "127.0.0.1", nil }
	t.Cleanup(func() { exposeCommand, netExposeAddress = previous, previousAddress })
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	return record
}

func TestNetExposeOptions(t *testing.T) {
	options, detach, err := parseNetExpose([]string{"3000"})
	if err != nil || detach || options.Port != 3000 || options.Max != time.Hour || options.Protection != "password" ||
		strings.Count(options.Secret, "-") != 3 || !strings.HasPrefix(options.Name, "hi-") || !strings.HasSuffix(options.Name, "-3000") {
		t.Fatalf("defaults: %+v %v", options, err)
	}
	if options, _, err := parseNetExpose([]string{"8080", "--public", "--max", "30m", "--name", "demo", "--detach"}); err != nil ||
		options.Protection != "public" || options.Max != 30*time.Minute || options.Name != "demo" {
		t.Fatalf("public: %+v %v", options, err)
	}
	for _, args := range [][]string{
		{"3000", "--max", "none"}, {"3000", "--max", "48h"}, {"3000", "--pin", "12"},
		{"3000", "--public", "--pin", "123456"}, {"3000", "--name", "Bad Name"}, {}, {"notaport"},
	} {
		if _, _, err := parseNetExpose(args); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
	args := netExposeArgs(netExposeOptions{Name: "demo", Protection: "groups", Groups: "devops"}, 4000)
	if strings.Join(args, " ") != "expose 4000 --with-name-prefix demo --with-user-groups devops" {
		t.Fatalf("args: %v", args)
	}
}

func TestNetExposeRelaysALocalServiceUntilMax(t *testing.T) {
	ts := newTestServer(t)
	ts.connectAs(t, "alice", "students")
	record := fakeNetbirdExpose(t, "")
	var hosts []string
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts = append(hosts, r.Host)
		w.Write([]byte("hello from " + r.URL.Path))
	}))
	defer service.Close()
	port := service.Listener.Addr().(interface{ String() string }).String()
	port = port[strings.LastIndex(port, ":")+1:]

	var stdout, stderr bytes.Buffer
	done := make(chan int)
	go func() { done <- runNetExpose([]string{port, "--host", "127.0.0.1", "--max", "2s", "--pin", "123456"}, strings.NewReader(""), &stdout, &stderr) }()
	var url string
	for i := 0; i < 50 && url == ""; i++ {
		time.Sleep(100 * time.Millisecond)
		if states := netExposeStates(); len(states) == 1 {
			url = states["hi-test-ab12"].URL
		}
	}
	if url == "" {
		t.Fatalf("no state:\n%s%s", stdout.String(), stderr.String())
	}
	response, err := http.Get(url + "/page")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "hello from /page" || !strings.HasPrefix(hosts[len(hosts)-1], "127.0.0.1:") {
		t.Fatalf("relay: %q, hosts %v", body, hosts)
	}
	code := <-done
	args, _ := os.ReadFile(record)
	if code != 0 || !strings.Contains(stdout.String(), "Exposing 127.0.0.1:"+port+" at http://127.0.0.1:") || !strings.Contains(stdout.String(), "PIN: 123456") ||
		!strings.Contains(stdout.String(), "GET    /page") || !strings.Contains(stderr.String(), "--max") ||
		!strings.Contains(string(args), "--with-pin 123456") {
		t.Fatalf("code %d\n%s%s\nargs %s", code, stdout.String(), stderr.String(), args)
	}
	if len(netExposeStates()) != 0 {
		t.Fatal("the state file outlived the exposure")
	}
}

func TestServerRecordsExposures(t *testing.T) {
	ts := newTestServer(t)
	ts.connectAs(t, "alice", "students")
	reportNetExpose(netExposeState{URL: "https://hi-demo-ab12.eu1.netbird.services", Port: 3000, Protection: "password",
		Expires: computeNow().Add(time.Hour)}, true)
	reportNetExpose(netExposeState{URL: "https://hi-demo-ab12.eu1.netbird.services", Port: 3000, Protection: "password"}, false)
	audit, _ := os.ReadFile(filepath.Join(ts.dir, "audit.jsonl"))
	if !strings.Contains(string(audit), `"action":"exposed","subject":"https://hi-demo-ab12.eu1.netbird.services"`) ||
		!strings.Contains(string(audit), `"action":"stopped exposing"`) || !strings.Contains(string(audit), "port 3000, password") {
		t.Fatalf("audit = %s", audit)
	}
	client, _, _ := dataClient()
	if err := client.call(http.MethodPost, "/v1/activity", apiActivity{Instance: "http://plain", Command: "expose", Open: true}, nil); err == nil {
		t.Fatal("a non-https exposure was recorded")
	}
}

func TestNetExposeExplainsPeerExposeOff(t *testing.T) {
	fakeNetbirdExpose(t, "echo 'Error: receive expose event: peer expose is not enabled for this account'\nexit 1\n")
	code, _, stderr := runHi("net", "expose", "3999", "--public")
	if code == 0 || !strings.Contains(stderr, "turns on Peer Expose in Settings > Clients") {
		t.Fatalf("code %d\n%s", code, stderr)
	}
	if code, _, stderr := runHi("net", "expose", "stop", "nothing"); code == 0 || !strings.Contains(stderr, "nothing named") {
		t.Fatalf("stop: %d %s", code, stderr)
	}
	if code, stdout, _ := runHi("net", "expose", "ls"); code != 0 || !strings.Contains(stdout, "Nothing is exposed") {
		t.Fatalf("ls: %d %s", code, stdout)
	}
}
