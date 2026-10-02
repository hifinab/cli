package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The live dashboard reads one snapshot of the server a second: what is
// running and what each user is doing with it, what waits for a decision,
// spend against budgets, and recent activity. Admins read it through the
// admin socket, wall screens through a viewer device, and users only their
// own part through their device.

type liveSnapshot struct {
	Now         time.Time      `json:"now"`
	Version     string         `json:"version"`
	Scope       string         `json:"scope"` // "all" or a user's name
	Running     []liveInstance `json:"running"`
	Waiting     []liveRequest  `json:"waiting"`
	Budgets     []liveBudget   `json:"budgets"`
	Activity    []liveEvent    `json:"activity"`
	RatePerHour float64        `json:"rate_per_hour"`
	Today       float64        `json:"today"`
	Month       float64        `json:"month"`
	MonthBudget float64        `json:"month_budget"`
	// Models is this month's use of the model hi server ai serves, per
	// user, and ModelsMonth its cost.
	Models      []liveModelUse `json:"models,omitempty"`
	ModelsMonth float64        `json:"models_month,omitempty"`
}

type liveModelUse struct {
	User     string  `json:"user"`
	Requests int     `json:"requests"`
	Cost     float64 `json:"cost"`
}

type liveInstance struct {
	Name     string    `json:"name"`
	User     string    `json:"user"`
	Group    string    `json:"group"`
	Agent    string    `json:"agent,omitempty"`
	Provider string    `json:"provider"`
	Hardware string    `json:"hardware"`
	Rate     string    `json:"rate"`
	Reason   string    `json:"reason,omitempty"`
	Request  string    `json:"request"`
	Started  time.Time `json:"started"`
	Deadline time.Time `json:"deadline"`
	Cost     float64   `json:"cost"`
	Doing    []string  `json:"doing,omitempty"`
}

type liveRequest struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	State    string    `json:"state"`
	User     string    `json:"user"`
	Group    string    `json:"group"`
	Name     string    `json:"name"`
	Hardware string    `json:"hardware"`
	Rate     string    `json:"rate"`
	Reason   string    `json:"reason,omitempty"`
	Seconds  int64     `json:"seconds"`
	Created  time.Time `json:"created"`
	Over     bool      `json:"over_budget,omitempty"`
}

type liveBudget struct {
	Group  string  `json:"group"`
	Spend  float64 `json:"spend"`
	Budget float64 `json:"budget"`
}

type liveEvent struct {
	Time time.Time `json:"time"`
	Text string    `json:"text"`
	User string    `json:"user,omitempty"`
}

// liveFeed keeps recent events in memory for the dashboard.
type liveFeed struct {
	mu     sync.Mutex
	events []liveEvent
	// doing counts open sessions per instance and command, such as ssh.
	doing map[string]map[string]int
}

const liveFeedSize = 60

func (f *liveFeed) add(event liveEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	if len(f.events) > liveFeedSize {
		f.events = f.events[len(f.events)-liveFeedSize:]
	}
}

// loadRecentAudit seeds the feed from the audit log when the server starts.
func (f *liveFeed) loadRecentAudit(dir string) {
	data, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > liveFeedSize {
		lines = lines[len(lines)-liveFeedSize:]
	}
	for _, line := range lines {
		var entry auditEntry
		if json.Unmarshal([]byte(line), &entry) == nil {
			f.add(auditEvent(entry))
		}
	}
}

// auditEvent turns an audit entry into a short line for the feed.
func auditEvent(entry auditEntry) liveEvent {
	text := entry.Actor + " " + entry.Action + " " + entry.Subject
	switch entry.Action {
	case "requested compute":
		text = entry.Actor + " requested " + strings.SplitN(entry.Detail, ":", 2)[0]
	case "started":
		text = "started " + entry.Subject
	case "stopped":
		if entry.Actor == "limit" {
			text = entry.Subject + " stopped at its time limit"
		} else {
			text = entry.Actor + " stopped " + entry.Subject
		}
	case "approved", "denied":
		text = entry.Actor + " " + entry.Action + " " + entry.Subject
		if strings.HasPrefix(entry.Actor, "policy") {
			text = "auto-approved " + entry.Subject
		}
	}
	// Only people become initials on a wall, not the server's own actors.
	user := entry.Actor
	switch {
	case user == "server", user == "limit", user == "gone", strings.HasPrefix(user, "policy"), strings.HasPrefix(user, "slack:"):
		user = ""
	}
	return liveEvent{Time: entry.Time, Text: text, User: user}
}

func (f *liveFeed) setDoing(instance, command string, open bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.doing == nil {
		f.doing = map[string]map[string]int{}
	}
	if f.doing[instance] == nil {
		f.doing[instance] = map[string]int{}
	}
	if open {
		f.doing[instance][command]++
	} else if f.doing[instance][command] > 0 {
		f.doing[instance][command]--
	}
}

func (f *liveFeed) doingFor(instance string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []string
	for command, count := range f.doing[instance] {
		if count > 0 {
			result = append(result, command)
		}
	}
	sort.Strings(result)
	return result
}

// liveSnapshot builds the dashboard's view: everything, or one user's part.
func (s *hiServer) liveSnapshot(user string) liveSnapshot {
	policy := s.policy()
	now := computeNow()
	snapshot := liveSnapshot{Now: now, Version: version, Scope: "all"}
	if user != "" {
		snapshot.Scope = user
	}
	mine := func(name string) bool { return user == "" || name == user }

	s.mu.Lock()
	groupOf := func(name string) string {
		if member, ok := s.state.Users[name]; ok {
			return member.Group
		}
		return ""
	}
	for _, lease := range s.state.Leases {
		if !mine(lease.User) {
			continue
		}
		instance := liveInstance{Name: lease.Name, User: lease.User, Group: groupOf(lease.User), Provider: lease.Provider,
			Hardware: lease.Hardware, Rate: lease.Rate, Request: lease.Request, Started: lease.Started, Deadline: lease.Deadline,
			Cost: hourlyRate(lease.Rate) * now.Sub(lease.Started).Hours()}
		if request, ok := s.state.Requests[lease.Request]; ok {
			instance.Reason, instance.Agent = request.Reason, request.Agent
		}
		snapshot.Running = append(snapshot.Running, instance)
		snapshot.RatePerHour += hourlyRate(lease.Rate)
	}
	for _, request := range s.state.Requests {
		if !mine(request.User) || (request.State != "pending" && request.State != "starting") {
			continue
		}
		snapshot.Waiting = append(snapshot.Waiting, liveRequest{ID: request.ID, Kind: request.Kind, State: request.State,
			User: request.User, Group: groupOf(request.User), Name: request.Name, Hardware: request.Hardware,
			Rate: request.Rate, Reason: request.Reason, Seconds: request.MaxSeconds, Created: request.Created,
			Over: request.OverBudget})
	}
	local := now.Local()
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	snapshot.Today = s.spendLocked(today, now, func(request *serverRequest) bool { return mine(request.User) })
	snapshot.Month = s.spendLocked(monthStart(now), now, func(request *serverRequest) bool { return mine(request.User) })
	if user == "" {
		names := make([]string, 0, len(policy.Groups))
		for name, rules := range policy.Groups {
			if rules.GroupMonthlyBudget > 0 {
				names = append(names, name)
				snapshot.MonthBudget += rules.GroupMonthlyBudget
			}
		}
		sort.Strings(names)
		for _, name := range names {
			spend := s.spendLocked(monthStart(now), now, func(request *serverRequest) bool { return groupOf(request.User) == name })
			snapshot.Budgets = append(snapshot.Budgets, liveBudget{Group: name, Spend: spend, Budget: policy.Groups[name].GroupMonthlyBudget})
		}
	} else if budget := s.budgetLocked(policy, user, now); budget.UserBudget > 0 {
		snapshot.MonthBudget = budget.UserBudget
		snapshot.Budgets = []liveBudget{{Group: user, Spend: budget.UserSpend, Budget: budget.UserBudget}}
	}
	s.mu.Unlock()

	for _, row := range readAIUsage(s.dir, monthStart(now), now) {
		if mine(row.User) || (user != "" && row.Owner == user) {
			snapshot.Models = append(snapshot.Models, liveModelUse{User: row.User, Requests: row.Requests, Cost: row.Cost})
			snapshot.ModelsMonth += row.Cost
		}
	}
	for i := range snapshot.Running {
		snapshot.Running[i].Doing = s.feed.doingFor(snapshot.Running[i].Name)
	}
	sort.Slice(snapshot.Running, func(i, j int) bool { return snapshot.Running[i].Started.Before(snapshot.Running[j].Started) })
	sort.Slice(snapshot.Waiting, func(i, j int) bool { return snapshot.Waiting[i].Created.Before(snapshot.Waiting[j].Created) })

	s.feed.mu.Lock()
	for i := len(s.feed.events) - 1; i >= 0 && len(snapshot.Activity) < 20; i-- {
		event := s.feed.events[i]
		if user == "" || event.User == user || strings.Contains(event.Text, user) {
			snapshot.Activity = append(snapshot.Activity, event)
		}
	}
	s.feed.mu.Unlock()
	return snapshot
}

// ---------------------------------------------------------------------------
// endpoints

type apiActivity struct {
	Instance string `json:"instance"`
	Command  string `json:"command"`
	Open     bool   `json:"open"`
}

// handleLive serves a viewer everything, and anyone else their own part.
func (s *hiServer) handleLive(w http.ResponseWriter, _ *http.Request, device serverDevice, _ []byte) {
	s.mu.Lock()
	user := s.state.Users[device.User]
	viewer := user != nil && user.Kind == "viewer"
	s.mu.Unlock()
	if viewer {
		writeJSON(w, http.StatusOK, s.liveSnapshot(""))
		return
	}
	writeJSON(w, http.StatusOK, s.liveSnapshot(device.User))
}

// handleActivity records that a user opened or closed ssh, a tunnel, logs,
// or a model server on one of their instances. Only the command and
// instance names are sent, never arguments or anything typed.
func (s *hiServer) handleActivity(w http.ResponseWriter, _ *http.Request, device serverDevice, body []byte) {
	var input apiActivity
	if err := json.Unmarshal(body, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "malformed activity")
		return
	}
	switch input.Command {
	case "ssh", "tunnel", "logs", "serve":
	case "init":
		// A project made from a server template: the template's name only.
		if !validServerName(strings.ReplaceAll(input.Instance, "/", "-")) {
			writeAPIError(w, http.StatusBadRequest, "malformed template name")
			return
		}
		s.feed.add(liveEvent{Time: computeNow(), Text: fmt.Sprintf("%s started a project from %s", device.User, input.Instance), User: device.User})
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	default:
		writeAPIError(w, http.StatusBadRequest, "unknown activity")
		return
	}
	if _, ok := s.ownLease(input.Instance, device.User); !ok {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("you have no instance named %q", input.Instance))
		return
	}
	s.feed.setDoing(input.Instance, input.Command, input.Open)
	verb := map[bool]string{true: "opened", false: "closed"}[input.Open]
	s.feed.add(liveEvent{Time: computeNow(), Text: fmt.Sprintf("%s %s %s to %s", device.User, verb, input.Command, input.Instance), User: device.User})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// viewers: devices that may only watch, such as a wall screen

func (s *hiServer) addViewer(name, key, actor string) error {
	if !validServerName(name) {
		return fmt.Errorf("invalid viewer name %q", name)
	}
	public, err := decodeDeviceKey(key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if existing, ok := s.state.Users[name]; ok && existing.Kind != "viewer" {
		s.mu.Unlock()
		return fmt.Errorf("%s is already a user; choose another name for the viewer", name)
	}
	s.state.Users[name] = &serverUser{Name: name, Group: "viewers", Kind: "viewer", Added: computeNow()}
	fingerprint := keyFingerprint(public)
	s.state.Devices[fingerprint] = &serverDevice{Fingerprint: fingerprint, PublicKey: key, User: name,
		Hostname: "(viewer)", Added: computeNow()}
	err = s.saveLocked()
	s.mu.Unlock()
	if err == nil {
		s.audit(actor, "added viewer", name, "")
	}
	return err
}

func serverViewerCommand(args []string, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("viewer", stderr)
	key := flags.String("key", "", "the device key from `hi connect key` on the viewing machine")
	as := flags.String("as", currentUserName(), "who is acting")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	switch {
	case len(positional) == 2 && positional[0] == "add" && *key != "":
		body := map[string]string{"as": *as, "name": positional[1], "key": *key}
		if err := adminCall(*dirFlag, http.MethodPost, "/admin/viewers", body, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s can now watch the dashboard, and do nothing else.\n", positional[1])
		return nil
	case len(positional) == 2 && positional[0] == "remove":
		if err := adminCall(*dirFlag, http.MethodDelete, "/admin/users/"+positional[1]+"?as="+*as, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Removed the viewer %s.\n", positional[1])
		return nil
	}
	return usageError{"usage: hi server viewer add <name> --key <device key> | remove <name>"}
}
