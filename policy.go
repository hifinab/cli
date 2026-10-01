package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A server's policy.json sets, per group, how long a start may run, which
// hardware it may use, what is approved without a person, and monthly
// budgets. Budgets never block: going over warns the user and the
// approvers, and the request goes to a person. With no policy.json every
// start needs a person and nothing else is limited.

type serverPolicy struct {
	Groups  map[string]groupPolicy `json:"groups"`
	Reports *reportPolicy          `json:"reports,omitempty"`
}

type groupPolicy struct {
	// MaxHours refuses starts and extensions that would run longer.
	MaxHours float64 `json:"max_hours,omitempty"`
	// Hardware, when set, is the only hardware the group may start.
	Hardware []string `json:"hardware,omitempty"`
	// AutoApprove approves a start without a person when it is within
	// these limits and the user and group are within budget.
	AutoApprove *autoApprove `json:"auto_approve,omitempty"`
	// UserMonthlyBudget and GroupMonthlyBudget are in US dollars.
	UserMonthlyBudget  float64 `json:"user_monthly_budget_usd,omitempty"`
	GroupMonthlyBudget float64 `json:"group_monthly_budget_usd,omitempty"`
	// TemplateSources, when set, are the only template sources the group's
	// devices see; an empty list hides them all.
	TemplateSources *[]string `json:"template_sources,omitempty"`
}

type autoApprove struct {
	MaxPricePerHour float64 `json:"max_price_per_hour"`
	MaxHours        float64 `json:"max_hours"`
}

// reportPolicy turns the Slack reports off; they are on by default.
type reportPolicy struct {
	Daily   *bool `json:"daily,omitempty"`
	Weekly  *bool `json:"weekly,omitempty"`
	Monthly *bool `json:"monthly,omitempty"`
}

const policyExample = `{
  "groups": {
    "staff": {
      "max_hours": 8,
      "auto_approve": { "max_price_per_hour": 1.00, "max_hours": 2 },
      "user_monthly_budget_usd": 100,
      "group_monthly_budget_usd": 300
    },
    "students": {
      "max_hours": 4,
      "hardware": ["l4", "rtx-4090", "rtx-a5000", "a40"],
      "user_monthly_budget_usd": 25,
      "group_monthly_budget_usd": 100
    },
    "agents": {
      "max_hours": 2,
      "user_monthly_budget_usd": 50
    }
  },
  "reports": { "daily": true, "weekly": true, "monthly": true }
}`

func policyPath(dir string) string { return filepath.Join(dir, "policy.json") }

// parsePolicy reads and checks a policy, rejecting unknown fields so a
// typo can't silently turn a limit off.
func parsePolicy(data []byte) (serverPolicy, error) {
	var policy serverPolicy
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, fmt.Errorf("policy: %w", err)
	}
	for name, group := range policy.Groups {
		switch {
		case !validServerName(name):
			return policy, fmt.Errorf("policy: invalid group name %q", name)
		case group.MaxHours < 0 || group.UserMonthlyBudget < 0 || group.GroupMonthlyBudget < 0:
			return policy, fmt.Errorf("policy: group %s has a negative limit", name)
		case group.AutoApprove != nil && (group.AutoApprove.MaxPricePerHour <= 0 || group.AutoApprove.MaxHours <= 0):
			return policy, fmt.Errorf("policy: group %s's auto_approve needs max_price_per_hour and max_hours above 0", name)
		case group.AutoApprove != nil && group.MaxHours > 0 && group.AutoApprove.MaxHours > group.MaxHours:
			return policy, fmt.Errorf("policy: group %s auto-approves longer than its max_hours", name)
		}
	}
	if policy.Groups == nil {
		policy.Groups = map[string]groupPolicy{}
	}
	return policy, nil
}

// policy reads policy.json each time, so edits apply without a restart. A
// broken file is reported and treated as no policy: every start then needs
// a person, which is the safe side.
func (s *hiServer) policy() serverPolicy {
	data, err := os.ReadFile(policyPath(s.dir))
	if err != nil {
		return serverPolicy{Groups: map[string]groupPolicy{}}
	}
	policy, err := parsePolicy(data)
	if err != nil {
		fmt.Fprintf(s.log, "%v; ignoring policy.json until it is fixed\n", err)
		return serverPolicy{Groups: map[string]groupPolicy{}}
	}
	return policy
}

func savePolicy(dir string, policy serverPolicy) error {
	data, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	path := policyPath(dir)
	if err := os.WriteFile(path+".tmp", append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func (r *reportPolicy) on(which string) bool {
	if r == nil {
		return true
	}
	flag := map[string]*bool{"daily": r.Daily, "weekly": r.Weekly, "monthly": r.Monthly}[which]
	return flag == nil || *flag
}

// ---------------------------------------------------------------------------
// spend

func monthStart(now time.Time) time.Time {
	local := now.Local()
	return time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, time.Local)
}

// requestCost is what a request's instance cost between from and to, at its
// hourly rate.
func requestCost(request serverRequest, from, to time.Time) float64 {
	if request.Kind != "compute" || request.Started.IsZero() {
		return 0
	}
	start, end := request.Started, request.Ended
	if end.IsZero() || end.After(to) {
		end = to
	}
	if start.Before(from) {
		start = from
	}
	if !end.After(start) {
		return 0
	}
	return hourlyRate(request.Rate) * end.Sub(start).Hours()
}

// spendLocked adds up what matching requests cost between from and to.
func (s *hiServer) spendLocked(from, to time.Time, match func(request *serverRequest) bool) float64 {
	var total float64
	for _, request := range s.state.Requests {
		if match(request) {
			total += requestCost(*request, from, to)
		}
	}
	return total
}

// budgetStatus is a user's and their group's spend this month against their
// budgets; a zero budget means none.
type budgetStatus struct {
	User, Group                string
	UserSpend, UserBudget      float64
	GroupSpend, GroupBudget    float64
	UserOver, GroupOver, IsSet bool
}

func (b budgetStatus) over() bool { return b.UserOver || b.GroupOver }

// text reads "This month: $42.10 of your $100 · staff $120 of $300".
func (b budgetStatus) text() string {
	parts := []string{fmt.Sprintf("This month: %s", formatDollars(b.UserSpend))}
	if b.UserBudget > 0 {
		parts[0] += fmt.Sprintf(" of %s's %s", b.User, formatDollars(b.UserBudget))
	}
	if b.GroupBudget > 0 {
		parts = append(parts, fmt.Sprintf("%s %s of %s", b.Group, formatDollars(b.GroupSpend), formatDollars(b.GroupBudget)))
	}
	text := strings.Join(parts, " · ")
	switch {
	case b.UserOver && b.GroupOver:
		text += ". Over both budgets."
	case b.UserOver:
		text += ". Over " + b.User + "'s monthly budget."
	case b.GroupOver:
		text += ". Over the " + b.Group + " group's monthly budget."
	}
	return text
}

func (s *hiServer) budgetLocked(policy serverPolicy, user string, now time.Time) budgetStatus {
	group := ""
	if record, ok := s.state.Users[user]; ok {
		group = record.Group
	}
	rules := policy.Groups[group]
	from := monthStart(now)
	status := budgetStatus{User: user, Group: group, UserBudget: rules.UserMonthlyBudget, GroupBudget: rules.GroupMonthlyBudget}
	status.IsSet = status.UserBudget > 0 || status.GroupBudget > 0
	status.UserSpend = s.spendLocked(from, now, func(request *serverRequest) bool { return request.User == user })
	status.GroupSpend = s.spendLocked(from, now, func(request *serverRequest) bool {
		member, ok := s.state.Users[request.User]
		return ok && member.Group == group
	})
	status.UserOver = status.UserBudget > 0 && status.UserSpend >= status.UserBudget
	status.GroupOver = status.GroupBudget > 0 && status.GroupSpend >= status.GroupBudget
	return status
}

// ---------------------------------------------------------------------------
// applying the policy to a request

// checkRequestLocked refuses what the group may never do, and says whether
// the policy approves the rest by itself.
func (s *hiServer) checkRequestLocked(policy serverPolicy, user, hardware, rate string, totalHours float64) (autoApproved string, budget budgetStatus, err error) {
	group := ""
	if record, ok := s.state.Users[user]; ok {
		group = record.Group
	}
	rules, hasRules := policy.Groups[group]
	budget = s.budgetLocked(policy, user, computeNow())
	if !hasRules {
		return "", budget, nil
	}
	if rules.MaxHours > 0 && totalHours > rules.MaxHours+1e-9 {
		return "", budget, serverUsageError{fmt.Sprintf("the %s group may run at most %s at a time; ask for less with --max",
			group, formatDuration(time.Duration(rules.MaxHours*float64(time.Hour))))}
	}
	if len(rules.Hardware) > 0 {
		allowed := false
		for _, name := range rules.Hardware {
			allowed = allowed || strings.EqualFold(name, hardware)
		}
		if !allowed {
			return "", budget, serverUsageError{fmt.Sprintf("the %s group may use: %s", group, strings.Join(rules.Hardware, ", "))}
		}
	}
	if auto := rules.AutoApprove; auto != nil && !budget.over() &&
		hourlyRate(rate) <= auto.MaxPricePerHour+1e-9 && totalHours <= auto.MaxHours+1e-9 {
		return fmt.Sprintf("policy (%s: up to $%.2f/h and %s)", group, auto.MaxPricePerHour,
			formatDuration(time.Duration(auto.MaxHours*float64(time.Hour)))), budget, nil
	}
	return "", budget, nil
}

// checkBudgets tells the approvers, once a month, when a user or group has
// gone over its budget.
func (s *hiServer) checkBudgets(now time.Time) {
	policy := s.policy()
	month := monthStart(now).Format("2006-01")
	var alerts []string
	s.mu.Lock()
	if s.state.Alerts == nil {
		s.state.Alerts = map[string]bool{}
	}
	from := monthStart(now)
	for _, user := range s.state.Users {
		rules := policy.Groups[user.Group]
		if rules.UserMonthlyBudget <= 0 {
			continue
		}
		spend := s.spendLocked(from, now, func(request *serverRequest) bool { return request.User == user.Name })
		key := month + "/user/" + user.Name
		if spend >= rules.UserMonthlyBudget && !s.state.Alerts[key] {
			s.state.Alerts[key] = true
			alerts = append(alerts, fmt.Sprintf("💸 %s (%s) has spent %s this month, over their %s budget.",
				user.Name, user.Group, formatDollars(spend), formatDollars(rules.UserMonthlyBudget)))
		}
	}
	for name, rules := range policy.Groups {
		if rules.GroupMonthlyBudget <= 0 {
			continue
		}
		spend := s.spendLocked(from, now, func(request *serverRequest) bool {
			member, ok := s.state.Users[request.User]
			return ok && member.Group == name
		})
		key := month + "/group/" + name
		if spend >= rules.GroupMonthlyBudget && !s.state.Alerts[key] {
			s.state.Alerts[key] = true
			alerts = append(alerts, fmt.Sprintf("💸 The %s group has spent %s this month, over its %s budget.",
				name, formatDollars(spend), formatDollars(rules.GroupMonthlyBudget)))
		}
	}
	if len(alerts) > 0 {
		s.saveLocked()
	}
	s.mu.Unlock()
	for _, alert := range alerts {
		s.audit("server", "over budget", month, alert)
		s.notifyChannel(alert + " Nothing is blocked; new requests from them need a person.")
	}
}

// ---------------------------------------------------------------------------
// reports

// spendRow is one line of a spend table.
type spendRow struct {
	Name   string
	Group  string
	Spend  float64
	Budget float64
}

// spendTableLocked is each user's spend between from and to, most first.
func (s *hiServer) spendTableLocked(policy serverPolicy, from, to time.Time) ([]spendRow, map[string]float64) {
	perUser := map[string]float64{}
	perGroup := map[string]float64{}
	for _, request := range s.state.Requests {
		cost := requestCost(*request, from, to)
		if cost == 0 {
			continue
		}
		perUser[request.User] += cost
		if member, ok := s.state.Users[request.User]; ok {
			perGroup[member.Group] += cost
		}
	}
	var rows []spendRow
	for name, spend := range perUser {
		row := spendRow{Name: name, Spend: spend}
		if member, ok := s.state.Users[name]; ok {
			row.Group = member.Group
			row.Budget = policy.Groups[member.Group].UserMonthlyBudget
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Spend > rows[j].Spend })
	return rows, perGroup
}

func describeSpendRows(rows []spendRow, limit int) string {
	var lines []string
	for i, row := range rows {
		if i == limit {
			lines = append(lines, fmt.Sprintf("…and %d more", len(rows)-limit))
			break
		}
		line := fmt.Sprintf("• %s (%s) %s", row.Name, row.Group, formatDollars(row.Spend))
		if row.Budget > 0 {
			line += " of " + formatDollars(row.Budget)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func describeGroupSpend(policy serverPolicy, perGroup map[string]float64) string {
	names := make([]string, 0, len(perGroup))
	for name := range perGroup {
		names = append(names, name)
	}
	sort.Strings(names)
	var lines []string
	for _, name := range names {
		line := fmt.Sprintf("• %s %s", name, formatDollars(perGroup[name]))
		if budget := policy.Groups[name].GroupMonthlyBudget; budget > 0 {
			line += " of " + formatDollars(budget)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// report builds the daily, weekly, or monthly summary for the period
// ending at end.
func (s *hiServer) report(which string, end time.Time) string {
	policy := s.policy()
	var from time.Time
	var title string
	switch which {
	case "daily":
		from = end.AddDate(0, 0, -1)
		title = "Yesterday, " + from.Format("Mon 2 Jan")
	case "weekly":
		from = end.AddDate(0, 0, -7)
		title = fmt.Sprintf("Last week, %s to %s", from.Format("2 Jan"), end.AddDate(0, 0, -1).Format("2 Jan"))
	default:
		from = end.AddDate(0, -1, 0)
		title = from.Format("January 2006")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, perGroup := s.spendTableLocked(policy, from, end)
	var total float64
	for _, row := range rows {
		total += row.Spend
	}
	starts, limitStops, pending := 0, 0, 0
	hardware := map[string]int{}
	for _, request := range s.state.Requests {
		if request.Kind == "compute" && !request.Started.Before(from) && request.Started.Before(end) {
			starts++
			hardware[request.Hardware]++
		}
		if request.StoppedBy == "limit" && !request.Ended.Before(from) && request.Ended.Before(end) {
			limitStops++
		}
		if request.State == "pending" {
			pending++
		}
	}
	lines := []string{fmt.Sprintf("📊 *%s*: %s on %d starts.", title, formatDollars(total), starts)}
	if limitStops > 0 {
		lines = append(lines, fmt.Sprintf("%d stopped at their time limit.", limitStops))
	}
	if pending > 0 {
		lines = append(lines, fmt.Sprintf("%d waiting for a decision now.", pending))
	}
	if len(rows) > 0 {
		lines = append(lines, "*By user*\n"+describeSpendRows(rows, 10))
	}
	if which != "daily" && len(perGroup) > 0 {
		lines = append(lines, "*By group*\n"+describeGroupSpend(policy, perGroup))
	}
	if which == "weekly" && len(hardware) > 0 {
		names := make([]string, 0, len(hardware))
		for name := range hardware {
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool { return hardware[names[i]] > hardware[names[j]] })
		var used []string
		for i, name := range names {
			if i == 5 {
				break
			}
			used = append(used, fmt.Sprintf("%s ×%d", name, hardware[name]))
		}
		lines = append(lines, "*Most used:* "+strings.Join(used, ", "))
		var silent []string
		for _, device := range s.state.Devices {
			if device.LastSeen.Before(end.AddDate(0, 0, -7)) {
				silent = append(silent, fmt.Sprintf("%s (%s)", device.Hostname, device.User))
			}
		}
		sort.Strings(silent)
		if len(silent) > 0 {
			lines = append(lines, "*Not seen for a week:* "+strings.Join(silent, ", "))
		}
	}
	return strings.Join(lines, "\n")
}

// maybeReport posts the reports that are due, at 09:00 server time: daily,
// weekly on Mondays, and monthly on the 1st.
func (s *hiServer) maybeReport(now time.Time) {
	if s.slack == nil {
		return
	}
	local := now.Local()
	if local.Hour() < 9 {
		return
	}
	policy := s.policy()
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	due := func(which, key string) bool {
		if !policy.Reports.on(which) {
			return false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.state.Reported == nil {
			s.state.Reported = map[string]string{}
		}
		if s.state.Reported[which] == key {
			return false
		}
		s.state.Reported[which] = key
		s.saveLocked()
		return true
	}
	if due("daily", today.Format("2006-01-02")) {
		s.notifyChannel(s.report("daily", today))
	}
	if today.Weekday() == time.Monday && due("weekly", today.Format("2006-01-02")) {
		s.notifyChannel(s.report("weekly", today))
	}
	if today.Day() == 1 && due("monthly", today.Format("2006-01")) {
		s.notifyChannel(s.report("monthly", today))
	}
}

// ---------------------------------------------------------------------------
// commands

func serverPolicyCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("policy", stderr)
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError{"usage: hi server policy show | edit | example | check"}
	}
	dir, err := serverDirectory(*dirFlag)
	if err != nil {
		return err
	}
	path := policyPath(dir)
	switch positional[0] {
	case "example":
		fmt.Fprintln(stdout, policyExample)
		return nil
	case "show", "check":
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stdout, "No policy: every start needs a person, and there are no budgets.")
			fmt.Fprintln(stdout, "Start from `hi server policy example`, then `hi server policy edit`.")
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := parsePolicy(data); err != nil {
			return err
		}
		if positional[0] == "check" {
			fmt.Fprintf(stdout, "%s is valid.\n", path)
			return nil
		}
		stdout.Write(data)
		return nil
	case "edit":
		original, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			original = []byte(policyExample + "\n")
		} else if err != nil {
			return err
		}
		draft := path + ".edit"
		if err := os.WriteFile(draft, original, 0o600); err != nil {
			return err
		}
		defer os.Remove(draft)
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "nano"
		}
		for {
			command := exec.Command("sh", "-c", editor+` "$1"`, "editor", draft)
			command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
			if err := command.Run(); err != nil {
				return fmt.Errorf("the editor failed: %w", err)
			}
			data, err := os.ReadFile(draft)
			if err != nil {
				return err
			}
			if _, err := parsePolicy(data); err != nil {
				fmt.Fprintf(stderr, "hi: %v\n", err)
				if !isTerminal(stdin) || confirm(stdin, stdout, false, "Edit again?") != nil {
					return errors.New("the policy was not changed")
				}
				continue
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Saved %s. It applies to the next request; no restart needed.\n", path)
			return nil
		}
	}
	return usageError{"usage: hi server policy show | edit | example | check"}
}

func serverSpendCommand(args []string, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("spend", stderr)
	since := flags.String("since", "", "how far back, such as 7d (default: this month)")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return usageError{"usage: hi server spend [--since 7d]"}
	}
	path := "/admin/spend"
	if *since != "" {
		window, err := parseLifetime(*since)
		if err != nil || window == noLimit {
			return usageError{fmt.Sprintf("invalid --since %q; use e.g. 24h or 7d", *since)}
		}
		path += fmt.Sprintf("?seconds=%d", int64(window/time.Second))
	}
	var result map[string]string
	if err := adminCall(*dirFlag, "GET", path, nil, &result); err != nil {
		return err
	}
	fmt.Fprintln(stdout, strings.ReplaceAll(result["text"], "•", "-"))
	return nil
}

// usersSummary lists users with their group, devices, and spend this month.
func (s *hiServer) usersSummary(now time.Time) string {
	policy := s.policy()
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.state.Users))
	for name := range s.state.Users {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "No users yet."
	}
	from := monthStart(now)
	var lines []string
	for _, name := range names {
		user := s.state.Users[name]
		line := fmt.Sprintf("• %s (%s", name, user.Group)
		if user.Kind == "agent" {
			line += ", agent of " + user.Owner
		}
		line += ")"
		spend := s.spendLocked(from, now, func(request *serverRequest) bool { return request.User == name })
		line += " · " + formatDollars(spend) + " this month"
		if budget := policy.Groups[user.Group].UserMonthlyBudget; budget > 0 {
			line += " of " + formatDollars(budget)
		}
		var devices []string
		for _, device := range s.state.Devices {
			if device.User == name {
				seen := "never seen"
				if !device.LastSeen.IsZero() {
					seen = "seen " + formatDuration(now.Sub(device.LastSeen)) + " ago"
				}
				devices = append(devices, fmt.Sprintf("%s, %s", device.Hostname, seen))
			}
		}
		sort.Strings(devices)
		if len(devices) > 0 {
			line += " · " + strings.Join(devices, "; ")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// recentAudit is the last few audit entries, optionally about one user.
func (s *hiServer) recentAudit(user string, count int) string {
	data, err := os.ReadFile(filepath.Join(s.dir, "audit.jsonl"))
	if err != nil {
		return "Nothing has happened yet."
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry auditEntry
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		text := fmt.Sprintf("%s %s %s %s", entry.Time.Local().Format("02 Jan 15:04"), entry.Actor, entry.Action, entry.Subject)
		if entry.Detail != "" {
			text += ": " + entry.Detail
		}
		if user != "" && entry.Actor != user && !strings.Contains(entry.Detail, user) && !strings.Contains(entry.Subject, user) {
			continue
		}
		lines = append(lines, text)
	}
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	if len(lines) == 0 {
		return "Nothing matches."
	}
	return strings.Join(lines, "\n")
}

// setBudget changes a group's monthly budget per user, or for the whole
// group, in policy.json.
func (s *hiServer) setBudget(group, amount string, total bool, actor string) (string, error) {
	var dollars float64
	if _, err := fmt.Sscanf(strings.TrimPrefix(amount, "$"), "%g", &dollars); err != nil || dollars < 0 {
		return "", fmt.Errorf("%q is not an amount in dollars", amount)
	}
	if !validServerName(group) {
		return "", fmt.Errorf("%q is not a group name", group)
	}
	policy := serverPolicy{Groups: map[string]groupPolicy{}}
	if data, err := os.ReadFile(policyPath(s.dir)); err == nil {
		parsed, err := parsePolicy(data)
		if err != nil {
			return "", fmt.Errorf("fix policy.json first: %w", err)
		}
		policy = parsed
	}
	rules := policy.Groups[group]
	what := "each user in " + group
	if total {
		rules.GroupMonthlyBudget = dollars
		what = "the " + group + " group"
	} else {
		rules.UserMonthlyBudget = dollars
	}
	policy.Groups[group] = rules
	if err := savePolicy(s.dir, policy); err != nil {
		return "", err
	}
	s.audit(actor, "set budget", group, fmt.Sprintf("%s: %s a month", what, formatDollars(dollars)))
	if dollars == 0 {
		return fmt.Sprintf("%s removed the monthly budget for %s.", actor, what), nil
	}
	return fmt.Sprintf("%s set the monthly budget for %s to %s.", actor, what, formatDollars(dollars)), nil
}
