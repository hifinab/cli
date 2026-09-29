package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slack-go/slack"
)

// fakeSlack records what the bridge sends to Slack.
type fakeSlack struct {
	mu         sync.Mutex
	posts      []string // channel messages, "ts: text+blocks"
	updates    map[string]string
	threads    []string
	ephemerals []string
	views      []slack.ModalViewRequest
	next       int
}

func newFakeSlack() *fakeSlack { return &fakeSlack{updates: map[string]string{}} }

func slackPayload(options []slack.MsgOption) (string, string) {
	_, values, _ := slack.UnsafeApplyMsgOptions("token", "C1", "https://slack.test/", options...)
	return values.Get("text") + " " + values.Get("blocks"), values.Get("thread_ts")
}

func (f *fakeSlack) PostMessage(channel string, options ...slack.MsgOption) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	text, thread := slackPayload(options)
	if thread != "" {
		f.threads = append(f.threads, thread+": "+text)
		return channel, thread, nil
	}
	f.next++
	ts := fmt.Sprintf("100.%d", f.next)
	f.posts = append(f.posts, ts+": "+text)
	return channel, ts, nil
}

func (f *fakeSlack) UpdateMessage(channel, ts string, options ...slack.MsgOption) (string, string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	text, _ := slackPayload(options)
	f.updates[ts] = text
	return channel, ts, text, nil
}

func (f *fakeSlack) PostEphemeral(channel, user string, options ...slack.MsgOption) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	text, _ := slackPayload(options)
	f.ephemerals = append(f.ephemerals, user+": "+text)
	return "1", nil
}

func (f *fakeSlack) OpenView(trigger string, view slack.ModalViewRequest) (*slack.ViewResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.views = append(f.views, view)
	return &slack.ViewResponse{}, nil
}

func (f *fakeSlack) snapshot() (posts []string, updates map[string]string, threads, ephemerals []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	updates = map[string]string{}
	for ts, text := range f.updates {
		updates[ts] = text
	}
	return append([]string(nil), f.posts...), updates, append([]string(nil), f.threads...), append([]string(nil), f.ephemerals...)
}

// withSlack gives the test server a Slack bridge with bob as an approver.
func withSlack(t *testing.T, ts *testServer) *fakeSlack {
	t.Helper()
	fake := newFakeSlack()
	ts.server.slack = newSlackBridge(ts.server, slackConfig{
		Channel: "C1", Approvers: map[string]string{"UBOB": "bob", "UALICE": "alice"},
	}, fake)
	return fake
}

func click(action, value, user string) slack.InteractionCallback {
	var callback slack.InteractionCallback
	callback.Type = slack.InteractionTypeBlockActions
	callback.User.ID = user
	callback.Channel.ID = "C1"
	callback.TriggerID = "trigger"
	callback.ActionCallback.BlockActions = []*slack.BlockAction{{ActionID: action, Value: value}}
	return callback
}

// eventually waits for Slack calls queued by background work.
func eventually(t *testing.T, ts *testServer, what string, check func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		ts.server.slack.flush()
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSlackApprovesStartsAndStopsFromButtons(t *testing.T) {
	ts := newTestServer(t)
	fake := withSlack(t, ts)
	ts.connectAs(t, "carol", "students")

	runHi("compute", "up", "--on", "runpod", "--gpu", "l4", "--name", "slack-job", "--max", "2h",
		"--reason", "thesis sweep", "--yes", "--no-wait")
	id := ts.pending(t, "compute")
	ts.server.slack.flush()
	posts, _, _, _ := fake.snapshot()
	last := posts[len(posts)-1]
	for _, want := range []string{"carol", "slack-job", "$0.49/h", "at most $0.98", "thesis sweep", `"action_id":"approve"`, `"action_id":"deny"`} {
		if !strings.Contains(last, want) {
			t.Fatalf("request message is missing %q:\n%s", want, last)
		}
	}
	messageTS := strings.SplitN(last, ":", 2)[0]

	ts.server.slack.handleInteraction(click("approve", id, "USTRANGER"))
	_, _, _, ephemerals := fake.snapshot()
	if len(ephemerals) == 0 || !strings.Contains(ephemerals[0], "Only approvers") {
		t.Fatalf("a non-approver was not refused: %v", ephemerals)
	}
	if state := ts.server.state.Requests[id].State; state != "pending" {
		t.Fatalf("a non-approver changed the request to %s", state)
	}

	ts.server.slack.handleInteraction(click("approve", id, "UBOB"))
	eventually(t, ts, "the running message and its thread note", func() bool {
		_, updates, threads, _ := fake.snapshot()
		started := false
		for _, thread := range threads {
			started = started || strings.Contains(thread, messageTS+": Started on l4")
		}
		return started && strings.Contains(updates[messageTS], "running") && strings.Contains(updates[messageTS], `"action_id":"stop"`)
	})
	_, updates, threads, _ := fake.snapshot()
	if !strings.Contains(updates[messageTS], "approved by bob") {
		t.Fatalf("running message: %s", updates[messageTS])
	}
	if len(threads) == 0 || !strings.Contains(threads[0], messageTS+": Approved by bob") {
		t.Fatalf("no approval note in the thread: %v", threads)
	}

	ts.server.slack.handleInteraction(click("stop", "slack-job", "UBOB"))
	eventually(t, ts, "the stopped message", func() bool {
		_, updates, _, _ := fake.snapshot()
		return strings.Contains(updates[messageTS], "stopped by bob")
	})
	if len(ts.fake.stopped) != 1 || ts.fake.stopped[0] != "slack-job" {
		t.Fatalf("stopped %v", ts.fake.stopped)
	}
}

func TestSlackRefusesSelfApprovalAndDeniesWithAReason(t *testing.T) {
	ts := newTestServer(t)
	fake := withSlack(t, ts)
	ts.connectAs(t, "alice", "staff")
	runHi("compute", "up", "--on", "runpod", "--gpu", "a100", "--name", "big", "--max", "8h",
		"--reason", "x", "--yes", "--no-wait")
	id := ts.pending(t, "compute")

	ts.server.slack.handleInteraction(click("approve", id, "UALICE"))
	_, _, _, ephemerals := fake.snapshot()
	if len(ephemerals) == 0 || !strings.Contains(ephemerals[len(ephemerals)-1], "own request") {
		t.Fatalf("alice approved her own request from Slack: %v", ephemerals)
	}

	ts.server.slack.handleInteraction(click("deny", id, "UBOB"))
	if len(fake.views) != 1 || fake.views[0].PrivateMetadata != id {
		t.Fatalf("deny did not open a form: %+v", fake.views)
	}
	var submit slack.InteractionCallback
	submit.Type = slack.InteractionTypeViewSubmission
	submit.User.ID = "UBOB"
	submit.View.CallbackID = "deny"
	submit.View.PrivateMetadata = id
	submit.View.State = &slack.ViewState{Values: map[string]map[string]slack.BlockAction{
		"reason": {"reason": {Value: "use an L4 for this"}},
	}}
	ts.server.slack.handleInteraction(submit)
	eventually(t, ts, "the denied message", func() bool {
		_, updates, _, _ := fake.snapshot()
		for _, text := range updates {
			if strings.Contains(text, "denied by bob: use an L4 for this") {
				return true
			}
		}
		return false
	})
	code, _, stderr := runHi("compute", "requests", id, "--wait")
	if code != exitDenied || !strings.Contains(stderr, "use an L4 for this") {
		t.Fatalf("the requester did not see the reason: %d %s", code, stderr)
	}
}

func TestSlackEnrollmentButtonsChooseTheGroup(t *testing.T) {
	ts := newTestServer(t)
	fake := withSlack(t, ts)
	runHi("connect", ts.url, "--user", "dave", "--no-wait")
	id := ts.pending(t, "enroll")
	ts.server.slack.flush()
	posts, _, _, _ := fake.snapshot()
	if len(posts) == 0 || !strings.Contains(posts[0], "Approve as student") || !strings.Contains(posts[0], id+"|students") {
		t.Fatalf("enrollment message: %v", posts)
	}
	ts.server.slack.handleInteraction(click("enroll", id+"|students", "UBOB"))
	if user := ts.server.state.Users["dave"]; user == nil || user.Group != "students" {
		t.Fatalf("dave was not added to students: %+v", user)
	}
}

func TestSlashCommandShowsStatusAndConfirmsStops(t *testing.T) {
	ts := newTestServer(t)
	withSlack(t, ts)
	now := time.Now()
	ts.server.state.Leases["job"] = &serverLease{Name: "job", Provider: "runpod", Hardware: "l4", Rate: "$0.49/h",
		User: "carol", Started: now.Add(-time.Hour), Deadline: now.Add(time.Hour)}

	encode := func(v any) string { data, _ := json.Marshal(v); return string(data) }
	response := encode(ts.server.slack.slashResponse(slack.SlashCommand{UserID: "UBOB", Text: "status"}))
	if !strings.Contains(response, "1 running, $0.49/h now. Nothing is waiting") || !strings.Contains(response, `"action_id":"stop"`) {
		t.Fatalf("/hi status: %s", response)
	}
	response = encode(ts.server.slack.slashResponse(slack.SlashCommand{UserID: "UBOB", Text: "stop all"}))
	if !strings.Contains(response, "Stop all 1 running instances?") || !strings.Contains(response, "stop_all") {
		t.Fatalf("/hi stop all: %s", response)
	}
	if len(ts.fake.stopped) != 0 {
		t.Fatal("/hi stop all stopped without a confirming click")
	}
	response = encode(ts.server.slack.slashResponse(slack.SlashCommand{UserID: "USTRANGER", Text: "status"}))
	if !strings.Contains(response, "Only approvers") {
		t.Fatalf("a non-approver used /hi: %s", response)
	}
}

func TestSlackWarnsNearTheLimitAndReportsUnleasedPods(t *testing.T) {
	ts := newTestServer(t)
	fake := withSlack(t, ts)
	now := time.Now()
	ts.server.state.Requests["r-1"] = &serverRequest{ID: "r-1", Kind: "compute", State: "running", Name: "near", SlackTS: "100.9"}
	ts.server.state.Leases["near"] = &serverLease{Name: "near", Provider: "runpod", Request: "r-1",
		Started: now.Add(-50 * time.Minute), Deadline: now.Add(10 * time.Minute)}
	ts.fake.instances["near"] = upRequest{name: "near"}
	ts.fake.instances["rogue"] = upRequest{name: "rogue"}

	ts.server.reconcile()
	ts.server.reconcile()
	ts.server.slack.flush()
	posts, _, threads, _ := fake.snapshot()
	warnings := 0
	for _, thread := range threads {
		if strings.Contains(thread, "100.9: near has used 80% of its time") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Fatalf("want one warning in the thread, got %d: %v", warnings, threads)
	}
	found := false
	for _, post := range posts {
		found = found || strings.Contains(post, "`rogue` is running on the organization's runpod account")
	}
	if !found {
		t.Fatalf("no unleased alert: %v", posts)
	}
}

func TestSlackSetupAndApproversWorkWithoutTheServerRunning(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "server")
	if code, _, stderr := runHi("server", "init", "--listen", "127.0.0.1:7475", "--dir", dir); code != 0 {
		t.Fatal(stderr)
	}
	if code, _, stderr := runHi("server", "approvers", "add", "U123", "--name", "bob", "--dir", dir); code == 0 ||
		!strings.Contains(stderr, "slack setup") {
		t.Fatalf("approvers before setup: %d %s", code, stderr)
	}
	if err := saveSlackConfig(dir, slackConfig{BotToken: "xoxb-1", AppToken: "xapp-1", Channel: "C1"}); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := runHi("server", "approvers", "add", "U123", "--name", "bob", "--dir", dir); code != 0 ||
		!strings.Contains(stdout, "as bob") {
		t.Fatalf("approvers add: %d %s%s", code, stdout, stderr)
	}
	code, stdout, _ := runHi("server", "approvers", "list", "--dir", dir)
	if code != 0 || !strings.Contains(stdout, "U123") || !strings.Contains(stdout, "bob") {
		t.Fatalf("approvers list: %s", stdout)
	}
	info, _ := os.Stat(slackConfigPath(dir))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("slack.json mode %v", info.Mode().Perm())
	}
	if code, stdout, _ := runHi("server", "slack", "manifest"); code != 0 || !strings.Contains(stdout, `"socket_mode_enabled": true`) {
		t.Fatalf("manifest: %s", stdout)
	}
}

func TestSlackMessagesKeepTheApproverAndRoundSmallCosts(t *testing.T) {
	now := time.Now()
	request := serverRequest{ID: "r-1", Kind: "compute", State: "stopped", User: "iman", Name: "quick",
		Provider: "runpod", Hardware: "l4", Rate: "$0.49/h", MaxSeconds: 600, DecidedBy: "hi", StoppedBy: "hi",
		Started: now.Add(-30 * time.Second), Ended: now}
	text, _ := renderSlackRequest(request, nil, "staff", true, now)
	if !strings.Contains(text, "approved by hi · stopped by hi · ran 30s, under $0.01") {
		t.Fatalf("stopped: %s", text)
	}
	request.State, request.Error = "failed", "l4 is sold out"
	if text, _ := renderSlackRequest(request, nil, "staff", true, now); !strings.Contains(text, "approved by hi · could not start") {
		t.Fatalf("failed: %s", text)
	}
}

func TestSoldOutFailureSaysWhatIsFree(t *testing.T) {
	ts := newTestServer(t)
	ts.connectAs(t, "alice", "staff")
	ts.server.fallback = priceBound{factor: 1.01}
	ts.fake.soldOut["l4"] = true
	runHi("compute", "up", "--on", "runpod", "--gpu", "l4", "--name", "nope", "--max", "1h", "--reason", "x", "--yes", "--no-wait")
	id := ts.pending(t, "compute")
	ts.server.decide(id, "bob", true, "", "")
	_, _, stderr := runHi("compute", "requests", id, "--wait", "--timeout", "5s")
	if !strings.Contains(stderr, "Free now with as much memory: rtx-3090 ($0.50/h), rtx-4090 ($0.74/h), a100 ($1.99/h)") {
		t.Fatalf("stderr %s", stderr)
	}
}

func TestSlashStopUnderstandsMentionsAndReplacesItsPrompt(t *testing.T) {
	ts := newTestServer(t)
	withSlack(t, ts)
	now := time.Now()
	ts.fake.instances["job"] = upRequest{name: "job"}
	ts.server.state.Leases["job"] = &serverLease{Name: "job", Provider: "runpod", Hardware: "l4", Rate: "$0.49/h",
		User: "iman", Started: now.Add(-time.Minute), Deadline: now.Add(time.Hour)}
	encode := func(v any) string { data, _ := json.Marshal(v); return string(data) }
	for _, text := range []string{"stop @iman", "stop user @iman", "stop user <@U123|iman>"} {
		response := encode(ts.server.slack.slashResponse(slack.SlashCommand{UserID: "UBOB", Text: text}))
		if !strings.Contains(response, "Stop iman's 1 instance?") || !strings.Contains(response, `"value":"iman"`) {
			t.Fatalf("/hi %s: %s", text, response)
		}
	}

	var replaced string
	previous := slackReplace
	slackReplace = func(url, text string) error { replaced = url + " " + text; return nil }
	defer func() { slackReplace = previous }()
	callback := click("stop_user", "iman", "UBOB")
	callback.Container.IsEphemeral = true
	callback.ResponseURL = "https://hooks.slack.test/1"
	ts.server.slack.handleInteraction(callback)
	if replaced != "https://hooks.slack.test/1 Stopped everything iman was running." {
		t.Fatalf("the private prompt was not replaced: %q", replaced)
	}
	if _, running := ts.server.state.Leases["job"]; running {
		t.Fatal("job is still leased")
	}
}

func TestSlackShowsUserTextAsTyped(t *testing.T) {
	request := serverRequest{ID: "r-1", Kind: "compute", State: "pending", User: "eve", Hostname: "<!channel>",
		Name: "x", Provider: "runpod", Hardware: "l4", Rate: "$0.49/h", MaxSeconds: 600,
		Reason: "ping @iman <!here> <https://evil.test|click> & more"}
	_, blocks := renderSlackRequest(request, nil, "staff", true, time.Now())
	var buffer strings.Builder
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.Encode(blocks)
	out := buffer.String()
	for _, bad := range []string{"<!here>", "<!channel>", "<https://evil.test"} {
		if strings.Contains(out, bad) {
			t.Fatalf("user text reached Slack unescaped (%s): %s", bad, out)
		}
	}
	if !strings.Contains(out, `"verbatim":true`) || !strings.Contains(out, "&lt;!here&gt;") {
		t.Fatalf("blocks: %s", out)
	}
}

func TestSlashCommandKnowsAboutStartingInstances(t *testing.T) {
	ts := newTestServer(t)
	withSlack(t, ts)
	ts.server.state.Requests["r-9"] = &serverRequest{ID: "r-9", Kind: "compute", State: "starting", User: "iman",
		Name: "warming", Provider: "runpod", Hardware: "a40"}
	encode := func(v any) string { data, _ := json.Marshal(v); return string(data) }
	if out := encode(ts.server.slack.slashResponse(slack.SlashCommand{UserID: "UBOB", Text: "status"})); !strings.Contains(out, "1 starting") ||
		!strings.Contains(out, "warming") {
		t.Fatalf("/hi status: %s", out)
	}
	if out := encode(ts.server.slack.slashResponse(slack.SlashCommand{UserID: "UBOB", Text: "stop @iman"})); !strings.Contains(out, "still starting") {
		t.Fatalf("/hi stop @iman: %s", out)
	}
}

func TestSlackSaysHowLongStartingTakes(t *testing.T) {
	request := serverRequest{ID: "r-1", Kind: "compute", State: "starting", User: "iman", Name: "x", Provider: "runpod",
		Hardware: "a40", Rate: "$0.49/h", MaxSeconds: 600, DecidedBy: "hi", Progress: "Created pod abc"}
	text, _ := renderSlackRequest(request, nil, "staff", true, time.Now())
	if !strings.Contains(text, "starting on runpod") || !strings.Contains(text, "1–3 minutes") ||
		!strings.Contains(text, "turns 🟢") || !strings.Contains(text, "Latest: Created pod abc") {
		t.Fatalf("starting message: %s", text)
	}
}

func TestRunningMessageShowsClockTimes(t *testing.T) {
	started := time.Date(2026, 9, 29, 12, 56, 0, 0, time.Local)
	request := serverRequest{ID: "r-1", Kind: "compute", State: "running", User: "iman", Name: "x", Provider: "runpod",
		Hardware: "a40", Rate: "$0.49/h", MaxSeconds: 600, DecidedBy: "hi"}
	lease := &serverLease{Name: "x", Started: started, Deadline: started.Add(10 * time.Minute)}
	text, _ := renderSlackRequest(request, lease, "staff", true, started.Add(time.Minute))
	if !strings.Contains(text, "running since 12:56, stops at 13:06") {
		t.Fatalf("running message: %s", text)
	}
}

func TestSlackFlagsCommunityCloudRequests(t *testing.T) {
	request := serverRequest{ID: "r-1", Kind: "compute", State: "pending", User: "sam", Name: "x", Provider: "runpod",
		Hardware: "rtx-4090@community", Rate: "$0.34/h", MaxSeconds: 3600}
	_, blocks := renderSlackRequest(request, nil, "students", true, time.Now())
	data, _ := json.Marshal(blocks)
	if !strings.Contains(string(data), "Community Cloud") || !strings.Contains(string(data), "No API tokens, passwords") {
		t.Fatalf("no Community Cloud warning: %s", data)
	}
}
