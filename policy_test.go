package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slack-go/slack"
)

func writePolicy(t *testing.T, ts *testServer, policy string) {
	t.Helper()
	if _, err := parsePolicy([]byte(policy)); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, policyPath(ts.dir), policy, 0o600)
}

const testPolicy = `{
  "groups": {
    "staff": {
      "max_hours": 8,
      "auto_approve": { "max_price_per_hour": 1.00, "max_hours": 2 },
      "user_monthly_budget_usd": 10,
      "group_monthly_budget_usd": 100
    },
    "students": { "max_hours": 4, "hardware": ["l4", "rtx-3090"], "user_monthly_budget_usd": 5 }
  }
}`

func TestPolicyRejectsTyposAndContradictions(t *testing.T) {
	for _, bad := range []string{
		`{"groups": {"staff": {"max_hour": 8}}}`,
		`{"groups": {"staff": {"max_hours": 2, "auto_approve": {"max_price_per_hour": 1, "max_hours": 4}}}}`,
		`{"groups": {"staff": {"auto_approve": {"max_price_per_hour": 0, "max_hours": 1}}}}`,
		`{"groups": {"Staff Group": {}}}`,
	} {
		if _, err := parsePolicy([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if _, err := parsePolicy([]byte(policyExample)); err != nil {
		t.Fatalf("the example is invalid: %v", err)
	}
}

func TestPolicyAutoApprovesWithinItsLimits(t *testing.T) {
	ts := newTestServer(t)
	writePolicy(t, ts, testPolicy)
	ts.connectAs(t, "alice", "staff")
	code, stdout, stderr := runHi("compute", "up", "--on", "runpod", "--gpu", "l4", "--name", "quick", "--max", "1h",
		"--reason", "x", "--yes", "--no-wait")
	if code != 0 || !strings.Contains(stdout, "Approved by policy (staff: up to $1.00/h and 2h)") || !strings.Contains(stdout, "quick is running") {
		t.Fatalf("auto-approval: code %d\n%s%s", code, stdout, stderr)
	}
	code, _, _ = runHi("compute", "up", "--on", "runpod", "--gpu", "a100", "--name", "pricey", "--max", "1h",
		"--reason", "x", "--yes", "--no-wait")
	if code != exitPending {
		t.Fatalf("a $1.99/h start was not left for a person: %d", code)
	}
	code, _, _ = runHi("compute", "up", "--on", "runpod", "--gpu", "l4", "--name", "long", "--max", "3h",
		"--reason", "x", "--yes", "--no-wait")
	if code != exitPending {
		t.Fatalf("a 3h start was not left for a person: %d", code)
	}
}

func TestPolicyRefusesWhatAGroupMayNeverDo(t *testing.T) {
	ts := newTestServer(t)
	writePolicy(t, ts, testPolicy)
	ts.connectAs(t, "sam", "students")
	code, _, stderr := runHi("compute", "up", "--on", "runpod", "--gpu", "l4", "--name", "a", "--max", "6h",
		"--reason", "x", "--yes", "--no-wait")
	if code != 1 || !strings.Contains(stderr, "students group may run at most 4h") {
		t.Fatalf("max_hours: %d %s", code, stderr)
	}
	code, _, stderr = runHi("compute", "up", "--on", "runpod", "--gpu", "a100", "--name", "b", "--max", "1h",
		"--reason", "x", "--yes", "--no-wait")
	if code != 1 || !strings.Contains(stderr, "students group may use: l4, rtx-3090") {
		t.Fatalf("hardware: %d %s", code, stderr)
	}
}

// spent records an instance that already ran, so the user has spent money.
func (ts *testServer) spent(user string, hours float64, rate string) {
	now := time.Now()
	id := newServerID("r")
	ts.server.state.Requests[id] = &serverRequest{ID: id, Kind: "compute", State: "stopped", User: user,
		Name: id, Rate: rate, Started: now.Add(-time.Duration(hours * float64(time.Hour))), Ended: now}
}

func TestOverBudgetWarnsButDoesNotBlock(t *testing.T) {
	ts := newTestServer(t)
	fake := withSlack(t, ts)
	writePolicy(t, ts, testPolicy)
	ts.connectAs(t, "alice", "staff")
	ts.spent("alice", 20, "$0.60/h") // $12 of a $10 budget

	code, stdout, stderr := runHi("compute", "up", "--on", "runpod", "--gpu", "l4", "--name", "more", "--max", "1h",
		"--reason", "x", "--yes", "--no-wait")
	if code != exitPending {
		t.Fatalf("over budget, a start within auto-approve limits should wait for a person: %d %s", code, stderr)
	}
	if !strings.Contains(stdout, "Warning: This month: $12.00 of alice's $10.00") || !strings.Contains(stdout, "Over alice's monthly budget") {
		t.Fatalf("the user was not warned:\n%s", stdout)
	}
	ts.server.slack.flush()
	posts, _, _, _ := fake.snapshot()
	if !strings.Contains(posts[len(posts)-1], "⚠️ This month: $12.00 of alice's $10.00") {
		t.Fatalf("the approvers were not warned: %s", posts[len(posts)-1])
	}

	ts.server.checkBudgets(time.Now())
	ts.server.checkBudgets(time.Now())
	ts.server.slack.flush()
	posts, _, _, _ = fake.snapshot()
	alerts := 0
	for _, post := range posts {
		if strings.Contains(post, "alice (staff) has spent $12.00 this month, over their $10.00 budget") {
			alerts++
		}
	}
	if alerts != 1 {
		t.Fatalf("want one budget alert, got %d", alerts)
	}
	code, stdout, _ = runHi("connect", "status")
	if code != 0 || !strings.Contains(stdout, "Warning: This month") {
		t.Fatalf("connect status did not show the budget:\n%s", stdout)
	}
}

func TestExtendGoesThroughApproval(t *testing.T) {
	ts := newTestServer(t)
	fake := withSlack(t, ts)
	ts.connectAs(t, "carol", "staff")
	runHi("compute", "up", "--on", "runpod", "--gpu", "l4", "--name", "job", "--max", "1h", "--reason", "x", "--yes", "--no-wait")
	ts.server.decide(ts.pending(t, "compute"), "bob", true, "", "")
	if code, _, stderr := runHi("compute", "requests", ts.lastRequest(t), "--wait", "--timeout", "5s"); code != 0 {
		t.Fatal(stderr)
	}
	before := ts.server.state.Leases["job"].Deadline

	code, stdout, stderr := runHi("compute", "extend", "job", "2h", "--reason", "needs more epochs", "--no-wait")
	if code != exitPending || !strings.Contains(stdout, "for 2h more on job") {
		t.Fatalf("extend: %d\n%s%s", code, stdout, stderr)
	}
	id := ts.pending(t, "extend")
	ts.server.slack.flush()
	posts, _, _, _ := fake.snapshot()
	if !strings.Contains(posts[len(posts)-1], "asks for more time: `job` 2h longer") {
		t.Fatalf("extend message: %s", posts[len(posts)-1])
	}
	ts.server.slack.handleInteraction(click("approve", id, "UBOB"))
	if code, stdout, _ := runHi("compute", "requests", id, "--wait", "--timeout", "5s"); code != 0 || !strings.Contains(stdout, "job runs 2h longer") {
		t.Fatalf("waiting on the extension: %d %s", code, stdout)
	}
	if got := ts.server.state.Leases["job"].Deadline.Sub(before); got != 2*time.Hour {
		t.Fatalf("the deadline moved by %s", got)
	}
	if code, _, stderr := runHi("compute", "extend", "nope", "1h", "--reason", "x"); code == 0 || !strings.Contains(stderr, "nope") {
		t.Fatalf("extending an unknown instance: %d %s", code, stderr)
	}
}

func (ts *testServer) lastRequest(t *testing.T) string {
	t.Helper()
	var latest *serverRequest
	for _, request := range ts.server.state.Requests {
		if latest == nil || request.Created.After(latest.Created) {
			latest = request
		}
	}
	return latest.ID
}

func TestAgentsAreLabelledAndCanEnrollOnTheirOwn(t *testing.T) {
	ts := newTestServer(t)
	fake := withSlack(t, ts)
	ts.connectAs(t, "iman", "staff")
	t.Setenv("HI_AGENT", "Claude Code")
	runHi("compute", "up", "--on", "runpod", "--gpu", "l4", "--name", "agentjob", "--max", "1h", "--reason", "x", "--yes", "--no-wait")
	ts.server.slack.flush()
	posts, _, _, _ := fake.snapshot()
	if !strings.Contains(posts[len(posts)-1], "*iman* via Claude Code (staff") {
		t.Fatalf("no agent label: %s", posts[len(posts)-1])
	}

	os.Remove(serverConnectionPath())
	os.Remove(deviceKeyPath())
	code, _, stderr := runHi("connect", ts.url, "--agent", "build-bot", "--owner", "iman", "--no-wait")
	if code != exitPending {
		t.Fatalf("agent connect: %d %s", code, stderr)
	}
	id := ts.pending(t, "enroll")
	ts.server.slack.flush()
	posts, _, _, _ = fake.snapshot()
	if !strings.Contains(posts[len(posts)-1], "Agent *build-bot* (owner iman)") || !strings.Contains(posts[len(posts)-1], id+"|agents") {
		t.Fatalf("agent enrollment message: %s", posts[len(posts)-1])
	}
	ts.server.slack.handleInteraction(click("enroll", id+"|agents", "UBOB"))
	if user := ts.server.state.Users["build-bot"]; user == nil || user.Kind != "agent" || user.Owner != "iman" || user.Group != "agents" {
		t.Fatalf("agent user: %+v", user)
	}
	if code, _, stderr := runHi("connect", ts.url, "--agent", "bot2"); code != 2 || !strings.Contains(stderr, "--owner") {
		t.Fatalf("an agent without an owner: %d %s", code, stderr)
	}
}

func TestReportsAndSlashCommandsForSpend(t *testing.T) {
	ts := newTestServer(t)
	fake := withSlack(t, ts)
	writePolicy(t, ts, testPolicy)
	ts.server.state.Users["alice"] = &serverUser{Name: "alice", Group: "staff"}
	ts.server.state.Users["sam"] = &serverUser{Name: "sam", Group: "students"}
	ts.spent("alice", 2, "$1.00/h")
	ts.spent("sam", 1, "$0.50/h")

	now := time.Now()
	report := ts.server.report("weekly", now.Add(time.Hour))
	for _, want := range []string{"$2.50 on 2 starts", "alice (staff) $2.00 of $10.00", "sam (students) $0.50 of $5.00", "staff $2.00 of $100.00"} {
		if !strings.Contains(report, want) {
			t.Fatalf("weekly report is missing %q:\n%s", want, report)
		}
	}
	morning := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, time.Local)
	ts.server.maybeReport(morning)
	ts.server.maybeReport(morning.Add(time.Hour))
	ts.server.slack.flush()
	posts, _, _, _ := fake.snapshot()
	daily := 0
	for _, post := range posts {
		if strings.Contains(post, "📊 *Yesterday") {
			daily++
		}
	}
	if daily != 1 {
		t.Fatalf("want one daily report, got %d: %v", daily, posts)
	}

	encode := func(v any) string { data, _ := json.Marshal(v); return string(data) }
	if out := encode(ts.server.slack.slashResponse(slack.SlashCommand{UserID: "UBOB", Text: "spend"})); !strings.Contains(out, "alice (staff) $2.00 of $10.00") {
		t.Fatalf("/hi spend: %s", out)
	}
	if out := encode(ts.server.slack.slashResponse(slack.SlashCommand{UserID: "UBOB", Text: "users"})); !strings.Contains(out, "sam (students)") {
		t.Fatalf("/hi users: %s", out)
	}
	out := encode(ts.server.slack.slashResponse(slack.SlashCommand{UserID: "UBOB", Text: "budget students 40"}))
	if !strings.Contains(out, "bob set the monthly budget for each user in students to $40.00") {
		t.Fatalf("/hi budget: %s", out)
	}
	if ts.server.policy().Groups["students"].UserMonthlyBudget != 40 || len(ts.server.policy().Groups["students"].Hardware) != 2 {
		t.Fatalf("policy after /hi budget: %+v", ts.server.policy().Groups["students"])
	}
	if out := encode(ts.server.slack.slashResponse(slack.SlashCommand{UserID: "UBOB", Text: "audit"})); !strings.Contains(out, "set budget students") {
		t.Fatalf("/hi audit: %s", out)
	}
}

func TestPolicyCommands(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "server")
	os.MkdirAll(dir, 0o700)
	if code, stdout, _ := runHi("server", "policy", "show", "--dir", dir); code != 0 || !strings.Contains(stdout, "No policy") {
		t.Fatalf("show without a policy: %s", stdout)
	}
	writeTestFile(t, policyPath(dir), `{"groups": {"staff": {"max_hourz": 1}}}`, 0o600)
	if code, _, stderr := runHi("server", "policy", "check", "--dir", dir); code == 0 || !strings.Contains(stderr, "max_hourz") {
		t.Fatalf("check accepted a typo: %s", stderr)
	}
	if code, stdout, _ := runHi("server", "policy", "example"); code != 0 || !strings.Contains(stdout, "auto_approve") {
		t.Fatalf("example: %s", stdout)
	}
}
