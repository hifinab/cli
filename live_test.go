package main

import (
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func liveFixture(t *testing.T) *testServer {
	t.Helper()
	ts := newTestServer(t)
	writePolicy(t, ts, testPolicy)
	now := time.Now()
	ts.server.state.Users["alice"] = &serverUser{Name: "alice", Group: "staff"}
	ts.server.state.Users["sam"] = &serverUser{Name: "sam", Group: "students"}
	ts.server.state.Requests["r-a"] = &serverRequest{ID: "r-a", Kind: "compute", State: "running", User: "alice",
		Name: "train", Rate: "$1.00/h", Reason: "secret project codename", Agent: "Claude Code", Started: now.Add(-48 * time.Minute)}
	ts.server.state.Leases["train"] = &serverLease{Name: "train", Provider: "runpod", Hardware: "l4", Rate: "$1.00/h",
		User: "alice", Request: "r-a", Started: now.Add(-48 * time.Minute), Deadline: now.Add(12 * time.Minute)}
	ts.server.state.Requests["r-s"] = &serverRequest{ID: "r-s", Kind: "compute", State: "pending", User: "sam",
		Name: "sweep", Hardware: "rtx-4090@community", Rate: "$0.34/h", Reason: "thesis", MaxSeconds: 7200, Created: now.Add(-3 * time.Minute)}
	ts.server.feed.add(liveEvent{Time: now, Text: "sam requested runpod/sweep", User: "sam"})
	return ts
}

func TestLiveSnapshotShowsEverythingOrOneUsersPart(t *testing.T) {
	ts := liveFixture(t)
	all := ts.server.liveSnapshot("")
	if len(all.Running) != 1 || len(all.Waiting) != 1 || all.RatePerHour != 1 || all.MonthBudget != 100 {
		t.Fatalf("snapshot: %+v", all)
	}
	if all.Running[0].Cost < 0.79 || all.Running[0].Cost > 0.81 || all.Running[0].Agent != "Claude Code" {
		t.Fatalf("running: %+v", all.Running[0])
	}
	mine := ts.server.liveSnapshot("sam")
	if len(mine.Running) != 0 || len(mine.Waiting) != 1 || mine.Scope != "sam" || len(mine.Activity) != 1 {
		t.Fatalf("sam's snapshot: %+v", mine)
	}
}

func TestLiveRenderOnAWallHidesNamesAndReasons(t *testing.T) {
	ts := liveFixture(t)
	snapshot := ts.server.liveSnapshot("")
	wall := renderLive(snapshot, liveOptions{wall: true}, liveState{width: 140, height: 40})
	for _, want := range []string{"1 running", "$1.00/h now", "train", "A. via Claude Code", "S.", "community", "sweep"} {
		if !strings.Contains(wall, want) && !(want == "sweep" && strings.Contains(wall, "rtx-4090@community")) {
			t.Fatalf("the wall is missing %q:\n%s", want, wall)
		}
	}
	for _, hidden := range []string{"alice", "secret project codename", "thesis", "approve"} {
		if strings.Contains(wall, hidden) {
			t.Fatalf("the wall shows %q:\n%s", hidden, wall)
		}
	}
	room := renderLive(snapshot, liveOptions{wall: true, fullNames: true, reasons: true}, liveState{width: 140, height: 40})
	if !strings.Contains(room, "alice") || !strings.Contains(room, "secret project codename") {
		t.Fatalf("--names full --reasons:\n%s", room)
	}
	admin := renderLive(snapshot, liveOptions{decide: true}, liveState{width: 140, height: 40})
	if !strings.Contains(admin, "a approve · d deny · s stop") || !strings.Contains(admin, "secret project codename") {
		t.Fatalf("admin view:\n%s", admin)
	}
	stale := renderLive(snapshot, liveOptions{wall: true}, liveState{width: 140, height: 40, stale: true, lastOK: time.Now()})
	if !strings.Contains(stale, "Reconnecting… last update") || !strings.Contains(stale, "train") {
		t.Fatalf("stale view:\n%s", stale)
	}
}

func TestLiveWallRotatesWhatDoesNotFit(t *testing.T) {
	ts := liveFixture(t)
	for i := 0; i < 20; i++ {
		ts.server.feed.add(liveEvent{Time: time.Now(), Text: "event"})
	}
	snapshot := ts.server.liveSnapshot("")
	first := renderLive(snapshot, liveOptions{wall: true}, liveState{width: 140, height: 14, rotate: 0})
	second := renderLive(snapshot, liveOptions{wall: true}, liveState{width: 140, height: 14, rotate: 1})
	if !strings.Contains(first, "train") || !strings.Contains(second, "train") {
		t.Fatal("the running panel must always show")
	}
	if first == second {
		t.Fatalf("panels did not rotate:\n%s", first)
	}
	if lines := strings.Count(first, "\n") + 1; lines > 14 {
		t.Fatalf("%d lines on a 14-line screen", lines)
	}
}

// fakeLiveSource records what the dashboard asked for.
type fakeLiveSource struct {
	snap    liveSnapshot
	stopped []string
	denied  []string
	decide  bool
}

func (f *fakeLiveSource) snapshot() (liveSnapshot, error) { return f.snap, nil }
func (f *fakeLiveSource) approve(id string) error         { return nil }
func (f *fakeLiveSource) deny(id, reason string) error {
	f.denied = append(f.denied, id+": "+reason)
	return nil
}
func (f *fakeLiveSource) stop(name string) error { f.stopped = append(f.stopped, name); return nil }
func (f *fakeLiveSource) canDecide() bool        { return f.decide }

func livePress(m *liveModel, keys ...string) {
	for _, key := range keys {
		var message tea.KeyMsg
		switch key {
		case "enter":
			message = tea.KeyMsg{Type: tea.KeyEnter}
		case "down":
			message = tea.KeyMsg{Type: tea.KeyDown}
		case " ":
			message = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
		default:
			message = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
		}
		_, command := m.Update(message)
		if command != nil {
			if done, ok := command().(liveDoneMsg); ok {
				m.Update(done)
			}
		}
	}
}

func TestLiveKeysStopAndDenyWithConfirmation(t *testing.T) {
	ts := liveFixture(t)
	source := &fakeLiveSource{snap: ts.server.liveSnapshot(""), decide: true}
	model := &liveModel{source: source, options: liveOptions{decide: true}}
	model.Update(liveDataMsg{snapshot: source.snap})

	livePress(model, "s", "n")
	if len(source.stopped) != 0 {
		t.Fatal("stopped without a yes")
	}
	livePress(model, "s", "y")
	if len(source.stopped) != 1 || source.stopped[0] != "train" {
		t.Fatalf("stopped %v", source.stopped)
	}
	livePress(model, "down", "d", "u", "s", "e", " ", "l", "4", "enter")
	if len(source.denied) != 1 || source.denied[0] != "r-s: use l4" {
		t.Fatalf("denied %v", source.denied)
	}

	wall := &liveModel{source: source, options: liveOptions{wall: true}}
	wall.Update(liveDataMsg{snapshot: source.snap})
	livePress(wall, "s", "y", "d")
	if len(source.stopped) != 1 {
		t.Fatal("a wall accepted a key")
	}
}

func TestViewersWatchEverythingAndCanDoNothingElse(t *testing.T) {
	ts := liveFixture(t)
	runHi("connect", "key")
	key, err := loadDeviceKey(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.server.addViewer("wall", devicePublicKey(key), "admin"); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := runHi("connect", ts.url, "--user", "wall"); code != 0 {
		t.Fatalf("viewer connect: %d %s%s", code, stdout, stderr)
	}
	code, stdout, stderr := runHi("server", "live", "--wall", "--once", "--width", "140", "--dir", filepath.Join(t.TempDir(), "none"))
	if code != 0 || !strings.Contains(stdout, "train") || !strings.Contains(stdout, "rtx-4090@community") {
		t.Fatalf("viewer wall: %d\n%s%s", code, stdout, stderr)
	}
	code, _, stderr = runHi("compute", "up", "--on", "runpod", "--gpu", "l4", "--max", "1h", "--reason", "x", "--yes", "--no-wait")
	if code == 0 || !strings.Contains(stderr, "a viewer can only watch") {
		t.Fatalf("a viewer requested compute: %d %s", code, stderr)
	}
}

func TestUsersSeeOnlyTheirOwnLiveViewAndReportActivity(t *testing.T) {
	ts := liveFixture(t)
	ts.connectAs(t, "carol", "staff")
	now := time.Now()
	ts.server.state.Leases["mine"] = &serverLease{Name: "mine", Provider: "runpod", Hardware: "l4", Rate: "$0.49/h",
		User: "carol", Started: now.Add(-time.Minute), Deadline: now.Add(time.Hour)}
	code, stdout, stderr := runHi("compute", "live", "--once", "--width", "140")
	if code != 0 || !strings.Contains(stdout, "mine") || strings.Contains(stdout, "train") || !strings.Contains(stdout, "your machines") {
		t.Fatalf("hi compute live: %d\n%s%s", code, stdout, stderr)
	}
	restore, _ := withManagedProviders()
	defer restore()
	provider, _ := providerByName("runpod")
	done := reportActivity(provider, "mine", "ssh")
	if doing := ts.server.feed.doingFor("mine"); len(doing) != 1 || doing[0] != "ssh" {
		t.Fatalf("doing %v", doing)
	}
	done()
	if doing := ts.server.feed.doingFor("mine"); len(doing) != 0 {
		t.Fatalf("still doing %v", doing)
	}
	// Someone else's instance can't be reported on.
	reportActivity(provider, "train", "ssh")
	if doing := ts.server.feed.doingFor("train"); len(doing) != 0 {
		t.Fatalf("carol reported on alice's instance: %v", doing)
	}
}

func TestServerLiveOnTheServerBoxUsesTheAdminSocket(t *testing.T) {
	ts := liveFixture(t)
	listener, err := net.Listen("unix", filepath.Join(ts.dir, "admin.sock"))
	if err != nil {
		t.Skip(err)
	}
	admin := &http.Server{Handler: ts.server.adminHandler()}
	go admin.Serve(listener)
	defer admin.Close()
	code, stdout, stderr := runHi("server", "live", "--once", "--width", "140", "--dir", ts.dir)
	if code != 0 || !strings.Contains(stdout, "a approve · d deny") || !strings.Contains(stdout, "alice") {
		t.Fatalf("admin live: %d\n%s%s", code, stdout, stderr)
	}
}

func TestWallUnitAndKeysLockScreensDown(t *testing.T) {
	unit := wallUnit("live", "")
	for _, want := range []string{"User=hi-wall", "tmux -L wall-live new-session -d -s live", "server live --wall", "Restart=always"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit is missing %q:\n%s", want, unit)
		}
	}
	if sized := wallUnit("tv", "240x67"); !strings.Contains(sized, "-x 240 -y 67") || !strings.Contains(sized, "window-size manual") {
		t.Fatalf("sized unit:\n%s", sized)
	}
	line, err := wallKeyLine("live", "office-tv", "ssh-ed25519 AAAAKEY someone@tv")
	if err != nil {
		t.Fatal(err)
	}
	want := `restrict,pty,command="/usr/bin/tmux -L wall-live attach -r -t live" ssh-ed25519 AAAAKEY hi-wall:live:office-tv`
	if line != want {
		t.Fatalf("key line:\n%s\nwant\n%s", line, want)
	}
	if _, err := wallKeyLine("live", "tv", "not a key"); err == nil {
		t.Fatal("accepted a non-key")
	}
	if code, _, stderr := runHi("server", "wall", "list"); code == 0 || !strings.Contains(stderr, "sudo") {
		t.Fatalf("wall without root: %d %s", code, stderr)
	}
}

func TestWallInitialsAreOnlyForPeople(t *testing.T) {
	event := auditEvent(auditEntry{Time: time.Now(), Actor: "limit", Action: "stopped", Subject: "train"})
	if event.User != "" || event.Text != "train stopped at its time limit" {
		t.Fatalf("event %+v", event)
	}
	snapshot := liveSnapshot{Now: time.Now(), Activity: []liveEvent{event,
		{Time: time.Now(), Text: "iman stopped train", User: "iman"},
		{Time: time.Now(), Text: "started iman-job", User: ""}}}
	out := renderLive(snapshot, liveOptions{wall: true}, liveState{width: 120, height: 40})
	if !strings.Contains(out, "train stopped at its time limit") || !strings.Contains(out, "I. stopped train") ||
		!strings.Contains(out, "started iman-job") {
		t.Fatalf("activity:\n%s", out)
	}
	if !strings.Contains(wallUnit("live", ""), "status off") {
		t.Fatal("the wall shows tmux's status bar")
	}
}
