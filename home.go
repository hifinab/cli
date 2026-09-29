package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"
)

// The Slack App Home is the dashboard inside Slack. Approvers see what runs,
// what waits, this month's spend, and devices; a linked user sees their own
// machines, requests, and budget. Linked users also get direct messages
// about their requests. A Slack account is linked to a hi user only from
// that user's own device, with a one-time code, so nobody can claim to be
// someone else in Slack.

// slackLinkTTL is how long a code from /hi link stays valid.
const slackLinkTTL = 10 * time.Minute

type pendingLink struct {
	slackUser string
	user      string
	expires   time.Time
}

type slackLinks struct {
	mu      sync.Mutex
	pending map[string]pendingLink
}

func newLinkCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	raw := make([]byte, 6)
	rand.Read(raw)
	for i := range raw {
		raw[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(raw)
}

// startLink answers /hi link <user> with a code for that user's device.
func (b *slackBridge) startLink(slackUser, user string) string {
	if !validServerName(user) {
		return "Usage: `/hi link <your hi user name>`"
	}
	code := newLinkCode()
	b.links.mu.Lock()
	if b.links.pending == nil {
		b.links.pending = map[string]pendingLink{}
	}
	for existing, link := range b.links.pending {
		if link.slackUser == slackUser || time.Now().After(link.expires) {
			delete(b.links.pending, existing)
		}
	}
	b.links.pending[code] = pendingLink{slackUser: slackUser, user: user, expires: time.Now().Add(slackLinkTTL)}
	b.links.mu.Unlock()
	return fmt.Sprintf("To link this Slack account to *%s*, run this on %s's device within 10 minutes:\n`hi connect slack %s`",
		slackEscape(user), slackEscape(user), code)
}

// finishLink links the Slack account that asked for the code, if the code
// was asked for this device's user.
func (s *hiServer) finishLink(code, user string) error {
	if s.slack == nil {
		return fmt.Errorf("this server has no Slack")
	}
	b := s.slack
	code = strings.ToUpper(strings.TrimSpace(code))
	b.links.mu.Lock()
	link, ok := b.links.pending[code]
	if ok {
		delete(b.links.pending, code)
	}
	b.links.mu.Unlock()
	if !ok || time.Now().After(link.expires) {
		return fmt.Errorf("that code is unknown or expired; type `/hi link %s` in Slack for a new one", user)
	}
	if link.user != user {
		return fmt.Errorf("that code was asked for %s, not %s", link.user, user)
	}
	config, err := loadSlackConfig(s.dir)
	if err != nil || config == nil {
		return fmt.Errorf("Slack is not set up on the server")
	}
	if config.Links == nil {
		config.Links = map[string]string{}
	}
	config.Links[link.slackUser] = user
	if err := saveSlackConfig(s.dir, *config); err != nil {
		return err
	}
	b.mu.Lock()
	b.config.Links = config.Links
	b.mu.Unlock()
	s.audit(user, "linked Slack", link.slackUser, "")
	b.enqueue(func() {
		b.api.PostMessage(link.slackUser, slack.MsgOptionText(fmt.Sprintf(
			"This Slack account is now linked to *%s*. You'll get messages here about your requests, "+
				"and the Home tab shows your machines.", slackEscape(user)), false))
	})
	return nil
}

// slackUserFor finds the Slack account linked to a hi user: a link made with
// /hi link, or an approver's mapping.
func (b *slackBridge) slackUserFor(user string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for slackUser, name := range b.config.Links {
		if name == user {
			return slackUser
		}
	}
	for slackUser, name := range b.config.Approvers {
		if name == user {
			return slackUser
		}
	}
	return ""
}

// linkedUser is the hi user behind a Slack account, if any.
func (b *slackBridge) linkedUser(slackUser string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if name, ok := b.config.Links[slackUser]; ok {
		return name, true
	}
	name, ok := b.config.Approvers[slackUser]
	return name, ok
}

// notifyUser sends a direct message to a linked user.
func (s *hiServer) notifyUser(user, text string) {
	if s.slack == nil {
		return
	}
	slackUser := s.slack.slackUserFor(user)
	if slackUser == "" {
		return
	}
	s.slack.enqueue(func() {
		if _, _, err := s.slack.api.PostMessage(slackUser, slack.MsgOptionText(slackEscape(text), false)); err != nil {
			fmt.Fprintf(s.log, "slack: message to %s: %v\n", user, err)
		}
	})
}

// ---------------------------------------------------------------------------
// the Home tab

func (b *slackBridge) publishHome(slackUser string) {
	s := b.server
	_, approver := b.approver(slackUser)
	user, linked := b.linkedUser(slackUser)
	var blocks []slack.Block
	switch {
	case approver:
		blocks = renderApproverHome(s.liveSnapshot(""), s.deviceRows(), computeNow())
	case linked:
		blocks = renderUserHome(s.liveSnapshot(user), user, computeNow())
	default:
		blocks = []slack.Block{
			slack.NewHeaderBlock(slackPlain("hi compute")),
			slack.NewSectionBlock(slackText("This Slack account isn't linked to a hi user yet. Type `/hi link <your hi user name>` "+
				"anywhere, then run the command it gives you on your own device."), nil, nil),
		}
	}
	view := slack.HomeTabViewRequest{Type: slack.VTHomeTab, Blocks: slack.Blocks{BlockSet: blocks}}
	if _, err := b.api.PublishView(slackUser, view, ""); err != nil {
		fmt.Fprintf(s.log, "slack: home for %s: %v\n", slackUser, err)
	}
}

type deviceRow struct {
	User, Hostname, Version string
	LastSeen                time.Time
}

func (s *hiServer) deviceRows() []deviceRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []deviceRow
	for _, device := range s.state.Devices {
		rows = append(rows, deviceRow{User: device.User, Hostname: device.Hostname, Version: device.Version, LastSeen: device.LastSeen})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].User+rows[i].Hostname < rows[j].User+rows[j].Hostname })
	return rows
}

func homeHeader(text string) slack.Block { return slack.NewHeaderBlock(slackPlain(text)) }

func homeContext(text string) slack.Block {
	return slack.NewContextBlock("", slackText(text))
}

func homeRunning(instance liveInstance, now time.Time, stop bool, showUser bool) slack.Block {
	who := ""
	if showUser {
		who = fmt.Sprintf(" · *%s*", slackEscape(instance.User))
		if instance.Agent != "" {
			who += " via " + slackEscape(instance.Agent)
		}
	}
	text := fmt.Sprintf("`%s`%s · %s/%s · %s · up %s, %s so far · stops %s", slackEscape(instance.Name), who,
		instance.Provider, slackEscape(instance.Hardware), instance.Rate, formatDuration(now.Sub(instance.Started)),
		formatDollars(instance.Cost), instance.Deadline.Local().Format("15:04"))
	if len(instance.Doing) > 0 {
		text += " · " + strings.Join(instance.Doing, ", ")
	}
	if isCommunityHardware(instance.Hardware) {
		text += "\n⚠️ Community Cloud: no tokens, passwords, or sensitive data."
	}
	var accessory *slack.Accessory
	if stop {
		accessory = slack.NewAccessory(stopButton(instance.Name))
	}
	return slack.NewSectionBlock(slackText(text), nil, accessory)
}

func homeBudgets(budgets []liveBudget) []slack.Block {
	var lines []string
	for _, budget := range budgets {
		share := 0.0
		if budget.Budget > 0 {
			share = budget.Spend / budget.Budget
		}
		filled := min(10, int(share*10+0.5))
		lines = append(lines, fmt.Sprintf("*%s* %s%s %s of %s", slackEscape(budget.Group), strings.Repeat("▰", filled),
			strings.Repeat("▱", 10-filled), formatDollars(budget.Spend), formatDollars(budget.Budget)))
	}
	if len(lines) == 0 {
		return nil
	}
	return []slack.Block{slack.NewSectionBlock(slackText(strings.Join(lines, "\n")), nil, nil)}
}

func renderApproverHome(snapshot liveSnapshot, devices []deviceRow, now time.Time) []slack.Block {
	blocks := []slack.Block{
		homeHeader("hi compute"),
		homeContext(fmt.Sprintf("%d running · %s/h now · %s today · %s this month · updated %s",
			len(snapshot.Running), formatDollars(snapshot.RatePerHour), formatDollars(snapshot.Today),
			formatDollars(snapshot.Month), now.Local().Format("15:04"))),
		homeHeader("Now"),
	}
	if len(snapshot.Running) == 0 {
		blocks = append(blocks, homeContext("Nothing is running."))
	}
	for _, instance := range snapshot.Running {
		blocks = append(blocks, homeRunning(instance, now, true, true))
	}
	if len(snapshot.Running) > 1 {
		blocks = append(blocks, slack.NewActionBlock("home-stop-all",
			slackButton("stop_all", "all", "Stop all…", slack.StyleDanger).WithConfirm(slack.NewConfirmationBlockObject(
				slackPlain("Stop everything?"), slackText(fmt.Sprintf("All %d running machines are terminated.", len(snapshot.Running))),
				slackPlain("Stop all"), slackPlain("Cancel")))))
	}
	blocks = append(blocks, homeHeader("Waiting"))
	if len(snapshot.Waiting) == 0 {
		blocks = append(blocks, homeContext("Nothing is waiting for a decision."))
	}
	for _, request := range snapshot.Waiting {
		what := request.Hardware + " for " + formatDuration(time.Duration(request.Seconds)*time.Second)
		if request.Kind == "extend" {
			what = fmt.Sprintf("`%s` %s longer", slackEscape(request.Name), formatDuration(time.Duration(request.Seconds)*time.Second))
		}
		state := "waiting " + formatDuration(now.Sub(request.Created))
		if request.State == "starting" {
			state = "🔵 approved, starting"
		}
		text := fmt.Sprintf("*%s* (%s) · %s · %s · %s\n> %s", slackEscape(request.User), request.Group,
			slackEscape(what), request.Rate, state, slackEscape(request.Reason))
		if isCommunityHardware(request.Hardware) {
			text += "\n⚠️ Community Cloud: a third-party host. No tokens, passwords, or sensitive data."
		}
		if request.Over {
			text += "\n⚠️ Over budget"
		}
		blocks = append(blocks, slack.NewSectionBlock(slackText(text), nil, nil))
		if request.State == "pending" {
			blocks = append(blocks, slack.NewActionBlock("home-"+request.ID,
				slackButton("approve", request.ID, "Approve", slack.StylePrimary),
				slackButton("deny", request.ID, "Deny…", slack.StyleDanger)))
		}
	}
	blocks = append(blocks, homeHeader("This month"))
	if budget := homeBudgets(snapshot.Budgets); budget != nil {
		blocks = append(blocks, budget...)
	} else {
		blocks = append(blocks, homeContext(fmt.Sprintf("%s spent; no group budgets are set.", formatDollars(snapshot.Month))))
	}
	blocks = append(blocks, homeHeader("Devices"))
	var lines []string
	for _, device := range devices {
		line := fmt.Sprintf("*%s* · %s", slackEscape(device.User), slackEscape(device.Hostname))
		switch {
		case device.LastSeen.IsZero():
			line += " · never seen"
		case now.Sub(device.LastSeen) > 7*24*time.Hour:
			line += fmt.Sprintf(" · 💤 silent %s", formatDuration(now.Sub(device.LastSeen)))
		default:
			line += fmt.Sprintf(" · seen %s ago", formatDuration(now.Sub(device.LastSeen)))
		}
		if device.Version != "" {
			line += " · hi " + slackEscape(device.Version)
			if olderRelease(device.Version, version) {
				line += " ⬆️ outdated"
			}
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = []string{"No devices yet."}
	}
	blocks = append(blocks, slack.NewSectionBlock(slackText(strings.Join(lines, "\n")), nil, nil))
	return blocks
}

func renderUserHome(snapshot liveSnapshot, user string, now time.Time) []slack.Block {
	blocks := []slack.Block{
		homeHeader("Your machines"),
		homeContext(fmt.Sprintf("%s · %s this month · updated %s", slackEscape(user), formatDollars(snapshot.Month), now.Local().Format("15:04"))),
	}
	if len(snapshot.Running) == 0 {
		blocks = append(blocks, homeContext("Nothing of yours is running. Start one with `hi compute up`."))
	}
	for _, instance := range snapshot.Running {
		blocks = append(blocks, homeRunning(instance, now, true, false))
	}
	blocks = append(blocks, homeHeader("Your requests"))
	if len(snapshot.Waiting) == 0 {
		blocks = append(blocks, homeContext("Nothing is waiting."))
	}
	for _, request := range snapshot.Waiting {
		state := "waiting for approval"
		if request.State == "starting" {
			state = "🔵 approved, starting"
		}
		blocks = append(blocks, slack.NewSectionBlock(slackText(fmt.Sprintf("`%s` · %s · %s · %s", slackEscape(request.Name),
			slackEscape(request.Hardware), request.Rate, state)), nil, nil))
	}
	if budget := homeBudgets(snapshot.Budgets); budget != nil {
		blocks = append(blocks, homeHeader("Your budget"))
		blocks = append(blocks, budget...)
	}
	return blocks
}

// ---------------------------------------------------------------------------
// the device side of linking

type apiSlackLink struct {
	Code string `json:"code"`
}

func (s *hiServer) handleSlackLink(w http.ResponseWriter, _ *http.Request, device serverDevice, body []byte) {
	var input apiSlackLink
	if err := json.Unmarshal(body, &input); err != nil || input.Code == "" {
		writeAPIError(w, http.StatusBadRequest, "give the code from /hi link")
		return
	}
	if err := s.finishLink(input.Code, device.User); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"linked": device.User})
}

func connectSlackCommand(args []string) error {
	if len(args) != 1 {
		return usageError{"usage: hi connect slack <code from /hi link in Slack>"}
	}
	client, err := connectedClient()
	if err != nil {
		return err
	}
	return client.call(http.MethodPost, "/v1/slack-link", apiSlackLink{Code: args[0]}, nil)
}

// unlink removes the caller's own link.
func (b *slackBridge) unlink(slackUser string) string {
	config, err := loadSlackConfig(b.server.dir)
	if err != nil || config == nil {
		return "Slack is not set up on the server."
	}
	user, ok := config.Links[slackUser]
	if !ok {
		return "This Slack account isn't linked."
	}
	delete(config.Links, slackUser)
	if err := saveSlackConfig(b.server.dir, *config); err != nil {
		return err.Error()
	}
	b.mu.Lock()
	b.config.Links = config.Links
	b.mu.Unlock()
	b.server.audit(user, "unlinked Slack", slackUser, "")
	return "Unlinked. You'll no longer get messages about " + slackEscape(user) + "'s requests."
}
