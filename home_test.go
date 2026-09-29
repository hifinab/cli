package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/slack-go/slack"
)

func slashText(t *testing.T, ts *testServer, user, text string) string {
	t.Helper()
	data, _ := json.Marshal(ts.server.slack.slashResponse(slack.SlashCommand{UserID: user, Text: text}))
	return string(data)
}

// link links a Slack member to the device's user through /hi link and
// hi connect slack, as a person would.
func link(t *testing.T, ts *testServer, slackUser, user string) {
	t.Helper()
	reply := slashText(t, ts, slackUser, "link "+user)
	start := strings.Index(reply, "hi connect slack ")
	if start < 0 {
		t.Fatalf("/hi link: %s", reply)
	}
	code := reply[start+len("hi connect slack ") : start+len("hi connect slack ")+6]
	if code, stdout, stderr := runHi("connect", "slack", code); code != 0 || !strings.Contains(stdout, "Linked") {
		t.Fatalf("hi connect slack: %d %s%s", code, stdout, stderr)
	}
}

func TestSlackLinkIsProvenFromTheUsersOwnDevice(t *testing.T) {
	ts := newTestServer(t)
	fake := withSlack(t, ts)
	ts.connectAs(t, "carol", "staff")

	reply := slashText(t, ts, "UCAROL", "link bob")
	code := reply[strings.Index(reply, "hi connect slack ")+len("hi connect slack "):][:6]
	if status, _, stderr := runHi("connect", "slack", code); status == 0 || !strings.Contains(stderr, "asked for bob, not carol") {
		t.Fatalf("carol's device linked someone as bob: %d %s", status, stderr)
	}
	if status, _, stderr := runHi("connect", "slack", "ZZZZZZ"); status == 0 || !strings.Contains(stderr, "unknown or expired") {
		t.Fatalf("an unknown code: %d %s", status, stderr)
	}
	link(t, ts, "UCAROL", "carol")
	config, _ := loadSlackConfig(ts.dir)
	if config == nil || config.Links["UCAROL"] != "carol" {
		t.Fatalf("links: %+v", config)
	}
	ts.server.slack.flush()
	posts, _, _, _ := fake.snapshot()
	if !strings.Contains(strings.Join(posts, "\n"), "now linked to *carol*") {
		t.Fatalf("no welcome message: %v", posts)
	}
	if reply := slashText(t, ts, "UCAROL", "unlink"); !strings.Contains(reply, "Unlinked") {
		t.Fatalf("/hi unlink: %s", reply)
	}
}

func TestLinkedUsersGetMessagesAndCanStopTheirOwn(t *testing.T) {
	ts := newTestServer(t)
	fake := withSlack(t, ts)
	ts.connectAs(t, "carol", "staff")
	link(t, ts, "UCAROL", "carol")

	runHi("compute", "up", "--on", "runpod", "--gpu", "l4", "--name", "mine", "--max", "1h", "--reason", "x", "--yes", "--no-wait")
	ts.server.slack.handleInteraction(click("approve", ts.pending(t, "compute"), "UBOB"))
	eventually(t, ts, "a message to carol that it started", func() bool {
		posts, _, _, _ := fake.snapshot()
		return strings.Contains(strings.Join(posts, "\n"), "`mine`: Started on l4")
	})

	now := time.Now()
	ts.server.state.Leases["theirs"] = &serverLease{Name: "theirs", Provider: "runpod", User: "alice",
		Started: now, Deadline: now.Add(time.Hour)}
	ts.fake.instances["theirs"] = upRequest{name: "theirs"}
	ts.server.slack.handleInteraction(click("stop", "theirs", "UCAROL"))
	if _, running := ts.server.state.Leases["theirs"]; !running {
		t.Fatal("carol stopped alice's machine")
	}
	ts.server.slack.handleInteraction(click("stop", "mine", "UCAROL"))
	if _, running := ts.server.state.Leases["mine"]; running {
		t.Fatal("carol could not stop her own machine")
	}

	runHi("compute", "up", "--on", "runpod", "--gpu", "l4", "--name", "nope", "--max", "1h", "--reason", "x", "--yes", "--no-wait")
	ts.server.decide(ts.pending(t, "compute"), "bob", false, "", "too long")
	ts.server.slack.flush()
	posts, _, _, _ := fake.snapshot()
	if !strings.Contains(strings.Join(posts, "\n"), "was denied by bob. Reason: too long") {
		t.Fatalf("no denial message: %v", posts)
	}
}

func TestAppHomeShowsEachPersonTheirOwnView(t *testing.T) {
	ts := liveFixture(t)
	fake := withSlack(t, ts)
	ts.connectAs(t, "carol", "staff")
	link(t, ts, "UCAROL", "carol")
	now := time.Now()
	ts.server.state.Leases["carols"] = &serverLease{Name: "carols", Provider: "runpod", Hardware: "l4", Rate: "$0.49/h",
		User: "carol", Started: now.Add(-time.Minute), Deadline: now.Add(time.Hour)}

	ts.server.slack.publishHome("UBOB")
	approver := fake.home("UBOB")
	for _, want := range []string{"Now", "Waiting", "This month", "Devices", "train", "carols", `"action_id":"approve"`,
		`"action_id":"stop"`, "Community Cloud", "carol"} {
		if !strings.Contains(approver, want) {
			t.Fatalf("the approver's home is missing %q:\n%s", want, approver)
		}
	}
	ts.server.slack.publishHome("UCAROL")
	mine := fake.home("UCAROL")
	if !strings.Contains(mine, "Your machines") || !strings.Contains(mine, "carols") || strings.Contains(mine, "train") ||
		strings.Contains(mine, `"action_id":"approve"`) {
		t.Fatalf("carol's home:\n%s", mine)
	}
	ts.server.slack.publishHome("USTRANGER")
	if stranger := fake.home("USTRANGER"); !strings.Contains(stranger, "/hi link") || strings.Contains(stranger, "train") {
		t.Fatalf("a stranger's home:\n%s", stranger)
	}
}

func TestDevicesRecordTheirHiVersion(t *testing.T) {
	ts := newTestServer(t)
	ts.connectAs(t, "carol", "staff")
	rows := ts.server.deviceRows()
	if len(rows) != 1 || rows[0].Version != version || rows[0].LastSeen.IsZero() {
		t.Fatalf("devices: %+v", rows)
	}
}
