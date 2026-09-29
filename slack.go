package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

// The Slack bridge posts every request to a private channel, updates the
// message as it moves on, and takes approvals, denials, and stops from
// buttons and /hi. It connects over Socket Mode, an outbound WebSocket, so
// the server needs no public address. Every click is checked against the
// approvers list when it happens.

const slackManifest = `{
  "display_information": { "name": "hi compute", "description": "Approve and control GPU compute started with hi" },
  "features": {
    "bot_user": { "display_name": "hi compute", "always_online": true },
    "slash_commands": [
      { "command": "/hi", "description": "GPU compute: status and stop", "usage_hint": "status | stop <name> | stop user <user> | stop all", "should_escape": false }
    ]
  },
  "oauth_config": { "scopes": { "bot": ["chat:write", "commands", "users:read", "im:write"] } },
  "settings": { "interactivity": { "is_enabled": true }, "socket_mode_enabled": true, "org_deploy_enabled": false, "token_rotation_enabled": false }
}`

// slackReplace replaces a private message a button was clicked in; tests
// replace it.
var slackReplace = func(responseURL, text string) error {
	return slack.PostWebhook(responseURL, &slack.WebhookMessage{Text: text, ReplaceOriginal: true})
}

// slackWarnAt is the share of an instance's lifetime after which its thread
// gets a warning.
const slackWarnAt = 0.8

type slackConfig struct {
	BotToken string `json:"bot_token"`
	AppToken string `json:"app_token"`
	Channel  string `json:"channel"`
	// Approvers maps Slack member IDs to hi user names, so nobody can
	// approve their own request from Slack either.
	Approvers map[string]string `json:"approvers"`
}

func slackConfigPath(dir string) string { return filepath.Join(dir, "slack.json") }

func loadSlackConfig(dir string) (*slackConfig, error) {
	data, err := os.ReadFile(slackConfigPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var config slackConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("read %s: %w", slackConfigPath(dir), err)
	}
	if config.Approvers == nil {
		config.Approvers = map[string]string{}
	}
	return &config, nil
}

func saveSlackConfig(dir string, config slackConfig) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	path := slackConfigPath(dir)
	if err := os.WriteFile(path+".tmp", append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// slackAPI is the part of Slack the bridge uses; tests replace it.
type slackAPI interface {
	PostMessage(channelID string, options ...slack.MsgOption) (string, string, error)
	UpdateMessage(channelID, timestamp string, options ...slack.MsgOption) (string, string, string, error)
	PostEphemeral(channelID, userID string, options ...slack.MsgOption) (string, error)
	OpenView(triggerID string, view slack.ModalViewRequest) (*slack.ViewResponse, error)
}

type slackBridge struct {
	server *hiServer
	api    slackAPI
	client *slack.Client

	mu     sync.Mutex
	config slackConfig

	queue chan func()
}

func newSlackBridge(server *hiServer, config slackConfig, api slackAPI) *slackBridge {
	bridge := &slackBridge{server: server, api: api, config: config, queue: make(chan func(), 256)}
	go func() {
		for job := range bridge.queue {
			job()
		}
	}()
	return bridge
}

// enqueue runs Slack calls one at a time, in order, off the request path.
func (b *slackBridge) enqueue(job func()) {
	select {
	case b.queue <- job:
	default:
		fmt.Fprintln(b.server.log, "slack: too many updates queued; dropping one")
	}
}

// flush waits for queued Slack calls; tests use it.
func (b *slackBridge) flush() {
	done := make(chan struct{})
	b.queue <- func() { close(done) }
	<-done
}

func (b *slackBridge) approver(slackUser string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	name, ok := b.config.Approvers[slackUser]
	return name, ok
}

func (b *slackBridge) channel() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.config.Channel
}

// ---------------------------------------------------------------------------
// notifications from the server

// notifyRequest posts or updates the request's message.
func (s *hiServer) notifyRequest(id string) {
	if s.slack == nil {
		return
	}
	s.slack.enqueue(func() { s.slack.syncRequest(id) })
}

// notifyThread adds a line to the request's thread.
func (s *hiServer) notifyThread(id, text string) {
	if s.slack == nil {
		return
	}
	s.slack.enqueue(func() {
		s.mu.Lock()
		request, ok := s.state.Requests[id]
		ts := ""
		if ok {
			ts = request.SlackTS
		}
		s.mu.Unlock()
		if ts == "" {
			return
		}
		if _, _, err := s.slack.api.PostMessage(s.slack.channel(), slack.MsgOptionText(slackEscape(text), false),
			slack.MsgOptionTS(ts)); err != nil {
			fmt.Fprintf(s.log, "slack: %v\n", err)
		}
	})
}

// notifyChannel posts a message of its own.
func (s *hiServer) notifyChannel(text string) {
	if s.slack == nil {
		return
	}
	s.slack.enqueue(func() {
		if _, _, err := s.slack.api.PostMessage(s.slack.channel(), slack.MsgOptionText(slackEscape(text), false)); err != nil {
			fmt.Fprintf(s.log, "slack: %v\n", err)
		}
	})
}

func (b *slackBridge) syncRequest(id string) {
	s := b.server
	s.mu.Lock()
	request, ok := s.state.Requests[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	copy := *request
	var lease *serverLease
	if found, ok := s.state.Leases[copy.Name]; ok && found.Request == copy.ID {
		leaseCopy := *found
		lease = &leaseCopy
	}
	user, known := s.state.Users[copy.User]
	group := ""
	if known {
		group = user.Group
	}
	s.mu.Unlock()

	text, blocks := renderSlackRequest(copy, lease, group, known, computeNow())
	options := []slack.MsgOption{slack.MsgOptionText(text, false), slack.MsgOptionBlocks(blocks...)}
	if copy.SlackTS != "" {
		if _, _, _, err := b.api.UpdateMessage(b.channel(), copy.SlackTS, options...); err != nil {
			fmt.Fprintf(s.log, "slack: update %s: %v\n", id, err)
		}
		return
	}
	// Post only what still needs attention; old requests stay quiet.
	switch copy.State {
	case "pending", "starting", "running":
	default:
		return
	}
	_, ts, err := b.api.PostMessage(b.channel(), options...)
	if err != nil {
		fmt.Fprintf(s.log, "slack: post %s: %v\n", id, err)
		return
	}
	s.mu.Lock()
	if request, ok := s.state.Requests[id]; ok {
		request.SlackTS = ts
		s.saveLocked()
	}
	s.mu.Unlock()
}

// ---------------------------------------------------------------------------
// rendering

// slackText is formatted text that Slack shows verbatim: it never turns
// @names, #channels, or URLs into mentions or links.
func slackText(text string) *slack.TextBlockObject {
	return slack.NewTextBlockObject(slack.MarkdownType, text, false, true)
}

// slackEscape makes text from users and providers show as typed, so a
// reason can't mention @channel or fake a link.
func slackEscape(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
}

func slackPlain(text string) *slack.TextBlockObject {
	return slack.NewTextBlockObject(slack.PlainTextType, text, false, false)
}

func slackButton(actionID, value, label string, style slack.Style) *slack.ButtonBlockElement {
	button := slack.NewButtonBlockElement(actionID, value, slackPlain(label))
	if style != "" {
		button = button.WithStyle(style)
	}
	return button
}

func stopButton(name string) *slack.ButtonBlockElement {
	return slackButton("stop", name, "Stop", slack.StyleDanger).WithConfirm(slack.NewConfirmationBlockObject(
		slackPlain("Stop "+name+"?"), slackText("It is terminated along with everything on its disk."),
		slackPlain("Stop it"), slackPlain("Cancel")))
}

// hourlyRate reads "$0.49/h".
func hourlyRate(rate string) float64 {
	var hourly float64
	fmt.Sscanf(rate, "$%f/h", &hourly)
	return hourly
}

func formatDollars(amount float64) string {
	return fmt.Sprintf("$%.2f", amount)
}

// aboutDollars describes a cost estimate, such as "about $0.04".
func aboutDollars(amount float64) string {
	if amount < 0.005 {
		return "under $0.01"
	}
	return "about " + formatDollars(amount)
}

// renderSlackRequest draws a request's message as it stands now.
func renderSlackRequest(request serverRequest, lease *serverLease, group string, knownUser bool, now time.Time) (string, []slack.Block) {
	for _, field := range []*string{&request.Hostname, &request.Reason, &request.DenyReason, &request.Error,
		&request.Image, &request.Progress, &request.Approved, &request.Name, &request.Hardware} {
		*field = slackEscape(*field)
	}
	who := fmt.Sprintf("*%s*", request.User)
	if group != "" {
		who = fmt.Sprintf("*%s* (%s, %s)", request.User, group, request.Hostname)
	}
	if request.Kind == "enroll" {
		switch request.State {
		case "pending":
			text := fmt.Sprintf("🟡 *%s* wants to join from *%s*\nDevice key `%s`", request.User, request.Hostname, request.Device)
			var buttons []slack.BlockElement
			if knownUser {
				text = fmt.Sprintf("🟡 *%s* (%s) wants to add a device: *%s*\nDevice key `%s`", request.User, group, request.Hostname, request.Device)
				buttons = append(buttons, slackButton("enroll", request.ID+"|", "Approve", slack.StylePrimary))
			} else {
				buttons = append(buttons,
					slackButton("enroll", request.ID+"|staff", "Approve as staff", slack.StylePrimary),
					slackButton("enroll", request.ID+"|students", "Approve as student", ""))
			}
			buttons = append(buttons, slackButton("deny", request.ID, "Deny…", slack.StyleDanger))
			return text, []slack.Block{slack.NewSectionBlock(slackText(text), nil, nil), slack.NewActionBlock("act-"+request.ID, buttons...)}
		case "approved":
			text := fmt.Sprintf("🟢 *%s* joined as %s from %s · approved by %s", request.User, request.Group, request.Hostname, request.DecidedBy)
			return text, []slack.Block{slack.NewSectionBlock(slackText(text), nil, nil)}
		case "denied":
			text := fmt.Sprintf("🔴 *%s* from %s was not let in · denied by %s", request.User, request.Hostname, request.DecidedBy)
			if request.DenyReason != "" {
				text += ": " + request.DenyReason
			}
			return text, []slack.Block{slack.NewSectionBlock(slackText(text), nil, nil)}
		default:
			text := fmt.Sprintf("⌛ *%s* from %s · %s with no decision", request.User, request.Hostname, request.State)
			return text, []slack.Block{slack.NewSectionBlock(slackText(text), nil, nil)}
		}
	}

	maximum := time.Duration(request.MaxSeconds) * time.Second
	cost := hourlyRate(request.Rate) * maximum.Hours()
	summary := fmt.Sprintf("*%s · %s* · %s · max %s → at most %s", request.Provider, request.Hardware, request.Rate,
		formatDuration(maximum), formatDollars(cost))
	if request.Image != "" {
		summary += " · image `" + request.Image + "`"
	}
	reason := "> " + strings.ReplaceAll(request.Reason, "\n", "\n> ")
	var status string
	var actions []slack.BlockElement
	switch request.State {
	case "pending":
		status = fmt.Sprintf("🟡 %s requests compute · `%s`", who, request.Name)
		actions = []slack.BlockElement{
			slackButton("approve", request.ID, "Approve", slack.StylePrimary),
			slackButton("deny", request.ID, "Deny…", slack.StyleDanger),
		}
	case "starting":
		status = fmt.Sprintf("🔵 %s · `%s` · approved by %s · starting on %s", who, request.Name, request.DecidedBy,
			request.Provider)
		note := "_Starting usually takes 1–3 minutes. The circle turns 🟢 when it's ready._"
		if request.Progress != "" {
			note += " Latest: " + request.Progress
		}
		status += "\n" + note
	case "running":
		status = fmt.Sprintf("🟢 %s · `%s` · approved by %s · running", who, request.Name, request.DecidedBy)
		if lease != nil {
			status += fmt.Sprintf(" %s, stops at %s", formatDuration(now.Sub(lease.Started)), lease.Deadline.Local().Format("15:04"))
			actions = []slack.BlockElement{stopButton(request.Name)}
		}
	case "stopped":
		ran := request.Ended.Sub(request.Started)
		status = fmt.Sprintf("⚪ %s · `%s` · approved by %s · stopped by %s · ran %s, %s", who, request.Name,
			request.DecidedBy, describeStopper(request.StoppedBy), formatDuration(ran),
			aboutDollars(hourlyRate(request.Rate)*ran.Hours()))
	case "denied":
		status = fmt.Sprintf("🔴 %s · `%s` · denied by %s", who, request.Name, request.DecidedBy)
		if request.DenyReason != "" {
			status += ": " + request.DenyReason
		}
	case "failed":
		status = fmt.Sprintf("❌ %s · `%s` · approved by %s · could not start: %s", who, request.Name, request.DecidedBy,
			strings.ReplaceAll(request.Error, "\n", " "))
	case "expired":
		status = fmt.Sprintf("⌛ %s · `%s` · expired with no decision", who, request.Name)
	default:
		status = fmt.Sprintf("%s · `%s` · %s", who, request.Name, request.State)
	}
	if request.Approved != "" {
		summary += fmt.Sprintf(" (approved %s, which was sold out)", request.Approved)
	}
	text := status + "\n" + summary + "\n" + reason
	blocks := []slack.Block{slack.NewSectionBlock(slackText(text), nil, nil)}
	if len(actions) > 0 {
		blocks = append(blocks, slack.NewActionBlock("act-"+request.ID, actions...))
	}
	return status, blocks
}

// slackName turns "@alice" or Slack's "<@U123|alice>" into "alice".
func slackName(field string) string {
	if strings.HasPrefix(field, "<@") && strings.HasSuffix(field, ">") {
		if _, name, found := strings.Cut(strings.TrimSuffix(field, ">"), "|"); found {
			return name
		}
	}
	return strings.TrimPrefix(field, "@")
}

func waitingText(pending int) string {
	switch pending {
	case 0:
		return "Nothing is waiting for a decision."
	case 1:
		return "1 request is waiting for a decision."
	}
	return fmt.Sprintf("%d requests are waiting for a decision.", pending)
}

func stopUserResponse(user string, leases []serverLease, respond func(string, ...slack.Block) map[string]any) map[string]any {
	count := 0
	for _, lease := range leases {
		if lease.User == user {
			count++
		}
	}
	if count == 0 {
		return respond(user + " has nothing running.")
	}
	noun := "instance"
	if count > 1 {
		noun = "instances"
	}
	text := fmt.Sprintf("Stop %s's %d %s?", user, count, noun)
	return respond(text, slack.NewSectionBlock(slackText(text), nil, nil),
		slack.NewActionBlock("stop-user", slackButton("stop_user", user, "Stop them", slack.StyleDanger)))
}

func describeStopper(stoppedBy string) string {
	switch stoppedBy {
	case "limit":
		return "its time limit"
	case "gone":
		return "the provider (it disappeared)"
	case "":
		return "someone"
	}
	return stoppedBy
}

// ---------------------------------------------------------------------------
// interactions

// handleInteraction acts on a button click or a submitted form. It runs
// after the Socket Mode request is acknowledged.
func (b *slackBridge) handleInteraction(callback slack.InteractionCallback) {
	s := b.server
	actor, ok := b.approver(callback.User.ID)
	reply := func(text string) {
		channel := callback.Channel.ID
		if channel == "" {
			channel = b.channel()
		}
		if _, err := b.api.PostEphemeral(channel, callback.User.ID, slack.MsgOptionText(text, false)); err != nil {
			fmt.Fprintf(s.log, "slack: %v\n", err)
		}
	}
	if !ok {
		s.audit("slack:"+callback.User.ID, "refused action", string(callback.Type), "not an approver")
		reply("Only approvers can do that. An admin adds approvers with `hi server approvers add`.")
		return
	}

	switch callback.Type {
	case slack.InteractionTypeViewSubmission:
		if callback.View.CallbackID != "deny" {
			return
		}
		reason := ""
		if block, ok := callback.View.State.Values["reason"]; ok {
			reason = strings.TrimSpace(block["reason"].Value)
		}
		if _, err := s.decide(callback.View.PrivateMetadata, actor, false, "", reason); err != nil {
			reply(err.Error())
		}
		return
	case slack.InteractionTypeBlockActions:
	default:
		return
	}
	if len(callback.ActionCallback.BlockActions) == 0 {
		return
	}
	action := callback.ActionCallback.BlockActions[0]
	var err error
	switch action.ActionID {
	case "approve":
		_, err = s.decide(action.Value, actor, true, "", "")
	case "enroll":
		id, group, _ := strings.Cut(action.Value, "|")
		_, err = s.decide(id, actor, true, group, "")
	case "deny":
		err = b.openDenyForm(callback.TriggerID, action.Value)
	case "stop":
		err = s.stopByName(action.Value, actor)
	case "stop_user":
		err = s.stopMatching(actor, func(lease serverLease) bool { return lease.User == action.Value })
	case "stop_all":
		err = s.stopMatching(actor, func(serverLease) bool { return true })
	}
	if err != nil {
		reply(err.Error())
		return
	}
	// A private /hi answer keeps no stale buttons: say what was done instead.
	if callback.Container.IsEphemeral && callback.ResponseURL != "" && strings.HasPrefix(action.ActionID, "stop") {
		done := map[string]string{
			"stop":      "Stopped `" + action.Value + "`.",
			"stop_user": "Stopped everything " + action.Value + " was running.",
			"stop_all":  "Stopped everything.",
		}[action.ActionID]
		if err := slackReplace(callback.ResponseURL, done); err != nil {
			fmt.Fprintf(s.log, "slack: %v\n", err)
		}
	}
}

func (b *slackBridge) openDenyForm(triggerID, id string) error {
	input := slack.NewPlainTextInputBlockElement(slackPlain("For example: use a 4090 instead"), "reason").WithMultiline(true)
	view := slack.ModalViewRequest{
		Type:            slack.VTModal,
		CallbackID:      "deny",
		PrivateMetadata: id,
		Title:           slackPlain("Deny " + id),
		Submit:          slackPlain("Deny"),
		Close:           slackPlain("Cancel"),
		Blocks: slack.Blocks{BlockSet: []slack.Block{
			slack.NewInputBlock("reason", slackPlain("Why? The requester sees this."), nil, input),
		}},
	}
	_, err := b.api.OpenView(triggerID, view)
	return err
}

func (s *hiServer) stopByName(name, actor string) error {
	s.mu.Lock()
	lease, ok := s.state.Leases[name]
	var copy serverLease
	if ok {
		copy = *lease
	}
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("%s is not running", name)
	}
	return s.stopLease(copy, actor)
}

func (s *hiServer) stopMatching(actor string, match func(serverLease) bool) error {
	s.mu.Lock()
	var chosen []serverLease
	for _, lease := range s.state.Leases {
		if match(*lease) {
			chosen = append(chosen, *lease)
		}
	}
	s.mu.Unlock()
	var failed []string
	for _, lease := range chosen {
		if err := s.stopLease(lease, actor); err != nil {
			failed = append(failed, err.Error())
		}
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

// slashResponse answers /hi privately. Anything that stops instances is a
// button, so it is confirmed and checked again when clicked.
func (b *slackBridge) slashResponse(command slack.SlashCommand) map[string]any {
	s := b.server
	respond := func(text string, blocks ...slack.Block) map[string]any {
		response := map[string]any{"response_type": "ephemeral", "text": text}
		if len(blocks) > 0 {
			response["blocks"] = blocks
		}
		return response
	}
	if _, ok := b.approver(command.UserID); !ok {
		s.audit("slack:"+command.UserID, "refused command", "/hi "+command.Text, "not an approver")
		return respond("Only approvers can use /hi. An admin adds approvers with `hi server approvers add`.")
	}
	fields := strings.Fields(command.Text)
	for i, field := range fields {
		fields[i] = slackName(field)
	}
	s.mu.Lock()
	leases := make([]serverLease, 0, len(s.state.Leases))
	for _, lease := range s.state.Leases {
		leases = append(leases, *lease)
	}
	pending := 0
	var starting []serverRequest
	for _, request := range s.state.Requests {
		switch request.State {
		case "pending":
			pending++
		case "starting":
			starting = append(starting, *request)
		}
	}
	s.mu.Unlock()
	sort.Slice(starting, func(i, j int) bool { return starting[i].Name < starting[j].Name })
	startingText := ""
	for _, request := range starting {
		startingText += fmt.Sprintf("\n`%s` · *%s* · %s/%s · starting", slackEscape(request.Name), request.User,
			request.Provider, slackEscape(request.Hardware))
	}
	sort.Slice(leases, func(i, j int) bool { return leases[i].Name < leases[j].Name })
	now := computeNow()

	switch {
	case len(fields) == 0 || fields[0] == "status":
		if len(leases) == 0 {
			text := "Nothing is running. " + waitingText(pending)
			if len(starting) > 0 {
				text = fmt.Sprintf("%d starting. %s%s", len(starting), waitingText(pending), startingText)
			}
			return respond(text, slack.NewSectionBlock(slackText(text), nil, nil))
		}
		var rate float64
		blocks := []slack.Block{}
		for _, lease := range leases {
			rate += hourlyRate(lease.Rate)
			line := fmt.Sprintf("`%s` · *%s* · %s/%s · %s · up %s, stops %s", lease.Name, lease.User, lease.Provider,
				lease.Hardware, lease.Rate, formatDuration(now.Sub(lease.Started)), lease.Deadline.Local().Format("15:04"))
			blocks = append(blocks, slack.NewSectionBlock(slackText(line), nil, slack.NewAccessory(stopButton(lease.Name))))
		}
		header := fmt.Sprintf("%d running, %s/h now. %s%s", len(leases), formatDollars(rate), waitingText(pending), startingText)
		return respond(header, append([]slack.Block{slack.NewSectionBlock(slackText(header), nil, nil)}, blocks...)...)
	case fields[0] == "stop" && len(fields) == 2 && fields[1] == "all":
		if len(leases) == 0 {
			return respond("Nothing is running.")
		}
		text := fmt.Sprintf("Stop all %d running instances?", len(leases))
		return respond(text, slack.NewSectionBlock(slackText(text), nil, nil),
			slack.NewActionBlock("stop-all", slackButton("stop_all", "all", "Stop all", slack.StyleDanger)))
	case fields[0] == "stop" && len(fields) == 3 && fields[1] == "user":
		return stopUserResponse(fields[2], leases, respond)
	case fields[0] == "stop" && len(fields) == 2:
		for _, lease := range leases {
			if lease.Name == fields[1] {
				text := fmt.Sprintf("`%s` · *%s* · %s/%s · up %s", lease.Name, lease.User, lease.Provider, lease.Hardware,
					formatDuration(now.Sub(lease.Started)))
				return respond(text, slack.NewSectionBlock(slackText(text), nil, slack.NewAccessory(stopButton(lease.Name))))
			}
		}
		// Not an instance: `/hi stop alice` means alice's instances.
		for _, lease := range leases {
			if lease.User == fields[1] {
				return stopUserResponse(fields[1], leases, respond)
			}
		}
		for _, request := range starting {
			if request.Name == fields[1] || request.User == fields[1] {
				return respond(fmt.Sprintf("`%s` is still starting. Stop it once it runs: its message in the channel "+
					"gets a Stop button, or try `/hi stop %s` again in a minute.", slackEscape(request.Name), slackEscape(fields[1])))
			}
		}
		return respond(fmt.Sprintf("Nothing called %s is running, and no user by that name has anything running. "+
			"See `/hi status`.", slackEscape(fields[1])))
	}
	return respond("Usage: `/hi status`, `/hi stop <name>`, `/hi stop user <user>`, `/hi stop all`")
}

// ---------------------------------------------------------------------------
// connection

// run keeps the Socket Mode connection up until the context ends.
func (b *slackBridge) run(ctx context.Context) {
	for ctx.Err() == nil {
		client := socketmode.New(b.client)
		go func() {
			for event := range client.Events {
				switch event.Type {
				case socketmode.EventTypeConnected:
					fmt.Fprintln(b.server.log, "slack: connected")
				case socketmode.EventTypeConnectionError:
					fmt.Fprintf(b.server.log, "slack: connection error: %v\n", event.Data)
				case socketmode.EventTypeInteractive:
					callback, ok := event.Data.(slack.InteractionCallback)
					if event.Request != nil {
						client.Ack(*event.Request)
					}
					if ok {
						go b.handleInteraction(callback)
					}
				case socketmode.EventTypeSlashCommand:
					command, ok := event.Data.(slack.SlashCommand)
					if event.Request == nil {
						continue
					}
					if !ok {
						client.Ack(*event.Request)
						continue
					}
					client.Ack(*event.Request, b.slashResponse(command))
				}
			}
		}()
		if err := client.RunContext(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(b.server.log, "slack: %v; reconnecting in 10s\n", err)
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
			}
		}
	}
}

// startSlack connects the bridge when slack.json exists.
func (s *hiServer) startSlack(ctx context.Context) error {
	config, err := loadSlackConfig(s.dir)
	if err != nil || config == nil {
		return err
	}
	client := slack.New(config.BotToken, slack.OptionAppLevelToken(config.AppToken))
	s.slack = newSlackBridge(s, *config, client)
	s.slack.client = client
	go s.slack.run(ctx)
	if len(config.Approvers) == 0 {
		fmt.Fprintln(s.log, "slack: warning: no approvers yet; add one with `hi server approvers add`")
	}
	return nil
}

// ---------------------------------------------------------------------------
// commands

func serverSlackCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("slack", stderr)
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	switch {
	case len(positional) == 1 && positional[0] == "manifest":
		fmt.Fprintln(stdout, slackManifest)
		return nil
	case len(positional) == 1 && positional[0] == "setup":
	default:
		return usageError{"usage: hi server slack setup | manifest"}
	}
	dir, err := serverDirectory(*dirFlag)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		return fmt.Errorf("no server in %s; run `hi server init` first", dir)
	}
	existing, err := loadSlackConfig(dir)
	if err != nil {
		return err
	}
	config := slackConfig{Approvers: map[string]string{}}
	if existing != nil {
		config.Approvers = existing.Approvers
	}
	if isTerminal(stdin) {
		fmt.Fprintln(stdout, "Create the Slack app from the manifest (`hi server slack manifest`) first.")
	}
	if config.BotToken, err = readSetting("Bot User OAuth Token (xoxb-…)", true, stdin, stdout); err != nil {
		return err
	}
	if config.AppToken, err = readSetting("App-Level Token with connections:write (xapp-…)", true, stdin, stdout); err != nil {
		return err
	}
	if config.Channel, err = readSetting("Channel ID of the private approvals channel (C…)", false, stdin, stdout); err != nil {
		return err
	}
	switch {
	case !strings.HasPrefix(config.BotToken, "xoxb-"):
		return errors.New("the bot token starts with xoxb-; find it under OAuth & Permissions")
	case !strings.HasPrefix(config.AppToken, "xapp-"):
		return errors.New("the app-level token starts with xapp-; create one under Basic Information with connections:write")
	case !strings.HasPrefix(config.Channel, "C") && !strings.HasPrefix(config.Channel, "G"):
		return errors.New("use the channel's ID, such as C0123456789, not its name; it is at the bottom of the channel's details")
	}
	client := slack.New(config.BotToken)
	auth, err := client.AuthTest()
	if err != nil {
		return fmt.Errorf("Slack rejected the bot token: %w", err)
	}
	if _, _, err := client.PostMessage(config.Channel, slack.MsgOptionText(
		"hi compute is connected. Requests to use GPUs will appear here for approval.", false)); err != nil {
		if strings.Contains(err.Error(), "not_in_channel") || strings.Contains(err.Error(), "channel_not_found") {
			return fmt.Errorf("the app can't post in %s; invite it with `/invite @%s` in the channel", config.Channel, auth.User)
		}
		return fmt.Errorf("post to the channel: %w", err)
	}
	if err := saveSlackConfig(dir, config); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Slack is set up for workspace %s as @%s, posting in %s.\n", auth.Team, auth.User, config.Channel)
	if len(config.Approvers) == 0 {
		fmt.Fprintln(stdout, "Add approvers: hi server approvers add <Slack member ID> --name <hi user name>")
	}
	fmt.Fprintln(stdout, "Restart the server to connect: sudo systemctl restart hi-server (or restart `hi server run`)")
	return nil
}

// readSetting reads one value: hidden in a terminal, or a line from stdin.
func readSetting(label string, secret bool, stdin io.Reader, stdout io.Writer) (string, error) {
	if file, ok := stdin.(*os.File); ok && isTerminal(stdin) {
		fmt.Fprintf(stdout, "%s: ", label)
		if secret {
			value, err := readSecret(file, stdout)
			fmt.Fprintln(stdout)
			return strings.TrimSpace(string(value)), err
		}
		line, err := readLine(stdin)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}
	line, err := readLine(stdin)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if strings.TrimSpace(line) == "" {
		return "", fmt.Errorf("no %s given", label)
	}
	return strings.TrimSpace(line), nil
}

func serverApproversCommand(args []string, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("approvers", stderr)
	name := flags.String("name", "", "the approver's hi user name")
	as := flags.String("as", currentUserName(), "who is acting")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	dir, err := serverDirectory(*dirFlag)
	if err != nil {
		return err
	}
	running := false
	if _, err := os.Stat(filepath.Join(dir, "admin.sock")); err == nil {
		running = true
	}
	var approvers map[string]string
	switch {
	case len(positional) == 1 && positional[0] == "list":
		if running {
			err = adminCall(*dirFlag, http.MethodGet, "/admin/approvers", nil, &approvers)
		} else if config, loadErr := loadSlackConfig(dir); loadErr != nil {
			err = loadErr
		} else if config != nil {
			approvers = config.Approvers
		}
		if err != nil {
			return err
		}
		if len(approvers) == 0 {
			fmt.Fprintln(stdout, "No approvers yet.")
			return nil
		}
		table := newTable(stdout)
		fmt.Fprintln(table, "SLACK MEMBER\tHI USER")
		ids := make([]string, 0, len(approvers))
		for id := range approvers {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			fmt.Fprintf(table, "%s\t%s\n", id, approvers[id])
		}
		return table.Flush()
	case len(positional) == 2 && positional[0] == "add":
		if *name == "" || !validServerName(*name) {
			return usageError{"usage: hi server approvers add <Slack member ID> --name <hi user name>"}
		}
		if !strings.HasPrefix(positional[1], "U") && !strings.HasPrefix(positional[1], "W") {
			return usageError{"use the Slack member ID, such as U0123456789: profile → ⋮ → Copy member ID"}
		}
	case len(positional) == 2 && positional[0] == "remove":
	default:
		return usageError{"usage: hi server approvers add <member ID> --name <user> | remove <member ID> | list"}
	}
	body := map[string]string{"as": *as, "id": positional[1], "name": *name, "action": positional[0]}
	if running {
		if err := adminCall(*dirFlag, http.MethodPost, "/admin/approvers", body, nil); err != nil {
			return err
		}
	} else {
		config, err := loadSlackConfig(dir)
		if err != nil {
			return err
		}
		if config == nil {
			return errors.New("Slack is not set up; run `hi server slack setup` first")
		}
		if positional[0] == "add" {
			config.Approvers[positional[1]] = *name
		} else {
			delete(config.Approvers, positional[1])
		}
		if err := saveSlackConfig(dir, *config); err != nil {
			return err
		}
	}
	if positional[0] == "add" {
		fmt.Fprintf(stdout, "%s can now approve and stop from Slack as %s.\n", positional[1], *name)
	} else {
		fmt.Fprintf(stdout, "%s can no longer approve or stop from Slack.\n", positional[1])
	}
	return nil
}

// approversHandler lets the admin CLI change approvers while the server runs.
func (s *hiServer) approversHandler(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/approvers", func(w http.ResponseWriter, _ *http.Request) {
		approvers := map[string]string{}
		if s.slack != nil {
			s.slack.mu.Lock()
			for id, name := range s.slack.config.Approvers {
				approvers[id] = name
			}
			s.slack.mu.Unlock()
		} else if config, err := loadSlackConfig(s.dir); err == nil && config != nil {
			approvers = config.Approvers
		}
		writeJSON(w, http.StatusOK, approvers)
	})
	mux.HandleFunc("POST /admin/approvers", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			As     string `json:"as"`
			ID     string `json:"id"`
			Name   string `json:"name"`
			Action string `json:"action"`
		}
		json.NewDecoder(io.LimitReader(r.Body, serverMaxBody)).Decode(&input)
		config, err := loadSlackConfig(s.dir)
		if err != nil || config == nil {
			writeAPIError(w, http.StatusBadRequest, "Slack is not set up; run `hi server slack setup` first")
			return
		}
		if input.Action == "add" {
			config.Approvers[input.ID] = input.Name
		} else {
			delete(config.Approvers, input.ID)
		}
		if err := saveSlackConfig(s.dir, *config); err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if s.slack != nil {
			s.slack.mu.Lock()
			s.slack.config.Approvers = config.Approvers
			s.slack.mu.Unlock()
		}
		s.audit(input.As, input.Action+" approver", input.ID, input.Name)
		writeJSON(w, http.StatusOK, map[string]string{"ok": input.ID})
	})
}
