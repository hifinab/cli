package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// managedProvider forwards a provider's hi compute calls to the hi server
// this device joined. The server holds the key, asks for approval, and
// enforces the limits; this device never sees the provider key.
type managedProvider struct {
	provider string
	info     apiProvider
	url      string

	mu      sync.Mutex
	client  *serverClient
	me      *apiMe
	meErr   error
	options []computeHardware
}

// managedPollEvery is how often a waiting request is checked.
var managedPollEvery = 3 * time.Second

// withManagedProviders swaps in managed drivers for the providers the
// server manages, and returns a function that puts the originals back. A
// device that never connected is unchanged. Colab is never managed.
func withManagedProviders() (func(), error) {
	connection, err := loadServerConnection()
	if err != nil || connection == nil || len(connection.Providers) == 0 {
		return func() {}, err
	}
	original := computeProviders
	providers := make([]computeProvider, 0, len(original)+len(connection.Providers))
	managed := map[string]bool{}
	for _, info := range connection.Providers {
		if info.Name != "colab" {
			managed[info.Name] = true
		}
	}
	seen := map[string]bool{}
	for _, provider := range original {
		if managed[provider.name()] {
			for _, info := range connection.Providers {
				if info.Name == provider.name() {
					providers = append(providers, &managedProvider{provider: info.Name, info: info, url: connection.URL})
				}
			}
		} else {
			providers = append(providers, provider)
		}
		seen[provider.name()] = true
	}
	for _, info := range connection.Providers {
		if managed[info.Name] && !seen[info.Name] {
			providers = append(providers, &managedProvider{provider: info.Name, info: info, url: connection.URL})
		}
	}
	computeProviders = providers
	return func() { computeProviders = original }, nil
}

func isManaged(provider computeProvider) bool {
	_, ok := provider.(*managedProvider)
	return ok
}

func (p *managedProvider) serverClient() (*serverClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		return p.client, nil
	}
	key, err := loadDeviceKey(false)
	if err != nil {
		return nil, err
	}
	p.client = newServerClient(p.url, key)
	return p.client, nil
}

func (p *managedProvider) call(method, path string, body, result any) error {
	client, err := p.serverClient()
	if err != nil {
		return err
	}
	return client.call(method, path, body, result)
}

// whoami asks the server once per command who this device is.
func (p *managedProvider) whoami() (*apiMe, error) {
	p.mu.Lock()
	if p.me != nil || p.meErr != nil {
		defer p.mu.Unlock()
		return p.me, p.meErr
	}
	p.mu.Unlock()
	client, err := p.serverClient()
	var me apiMe
	if err == nil {
		me, err = client.me()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.meErr = err
		return nil, err
	}
	p.me = &me
	// Keep the saved list of managed providers current.
	if connection, err := loadServerConnection(); err == nil && connection != nil {
		connection.Group, connection.Providers = me.Group, me.Providers
		saveServerConnection(*connection)
	}
	return p.me, nil
}

func (p *managedProvider) name() string { return p.provider }

// check reports the provider as ready only when the server answers. When it
// doesn't, nothing starts: hi never falls back to a personal key.
func (p *managedProvider) check() providerStatus {
	if _, err := p.whoami(); err != nil {
		return providerStatus{installed: true, hints: []string{
			"managed by " + p.url + ": " + err.Error(),
		}}
	}
	return providerStatus{installed: true, signedIn: true}
}

func (p *managedProvider) hardware() ([]computeHardware, error) {
	p.mu.Lock()
	cached := p.options
	p.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	var options []apiHardware
	if err := p.call(http.MethodGet, "/v1/hardware?provider="+url.QueryEscape(p.provider), nil, &options); err != nil {
		return nil, err
	}
	hardware := make([]computeHardware, len(options))
	for i, option := range options {
		hardware[i] = computeHardware{name: option.Name, kind: option.Kind, memory: option.Memory,
			rate: option.Rate, paid: option.Paid, note: option.Note}
	}
	p.mu.Lock()
	p.options = hardware
	p.mu.Unlock()
	return hardware, nil
}

func (p *managedProvider) maxLifetime() time.Duration {
	return time.Duration(p.info.MaxLifetime) * time.Second
}

// The server stops instances at --max, so no local watcher is needed.
func (*managedProvider) enforcesLifetime() bool { return true }

func (*managedProvider) reservedPorts() map[int]string { return nil }

func (p *managedProvider) account() (string, error) {
	me, err := p.whoami()
	if err != nil {
		return "", nil
	}
	text := fmt.Sprintf("%s is managed by %s: you are %s (%s), and starting needs an approval.",
		p.provider, p.url, me.User, me.Group)
	if me.Budget != "" {
		text += "\n" + me.Budget
	}
	return text, nil
}

func (p *managedProvider) validateRun(runRequest) error {
	return usageError{fmt.Sprintf("runs on the managed %s are not supported yet; start an instance with "+
		"`hi compute up --on %s` and run your script over `hi compute ssh`", p.provider, p.provider)}
}

func (*managedProvider) runCommand(runRequest) []string { return nil }

func (*managedProvider) runJob(runRequest, io.Reader, io.Writer, io.Writer) (int, error) {
	return 0, errors.New("managed runs are not supported yet")
}

func (*managedProvider) wait(string, io.Writer, io.Writer) (int, error) {
	return 0, errors.New("managed instances have no runs to wait for; see `hi compute requests`")
}

// prepareManagedStart collects what the server needs before anything is
// sent: a time limit, a reason for the approvers, and the SSH key the
// instance will accept.
func prepareManagedStart(request *upRequest, stdin io.Reader, stdout io.Writer) error {
	if request.max == noLimit {
		return usageError{"instances started through the hi server need a time limit; set --max"}
	}
	request.reason = strings.TrimSpace(request.reason)
	if request.reason == "" {
		if !isTerminal(stdin) {
			return usageError{"approvers need a reason; add --reason \"what this is for\""}
		}
		fmt.Fprint(stdout, "What is it for? (approvers see this) ")
		line, err := readLine(stdin)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if request.reason = strings.TrimSpace(line); request.reason == "" {
			return errors.New("cancelled; approvers need a reason")
		}
	}
	request.publicKey = runpodPublicKey()
	if request.publicKey == "" {
		return errors.New("no SSH public key found; run `ssh-keygen -t ed25519` first")
	}
	return nil
}

func (p *managedProvider) computeRequest(request upRequest) apiComputeRequest {
	return apiComputeRequest{
		Provider: p.provider, Hardware: request.hardware.name, Name: request.name,
		MaxSeconds: int64(request.max / time.Second), Image: request.image,
		PublicKey: request.publicKey, Reason: request.reason, Agent: agentName(),
	}
}

// agentName names the coding agent running hi, so approvers see who is
// really asking: $HI_AGENT, or an agent's own environment variable.
func agentName() string {
	if name := strings.TrimSpace(os.Getenv("HI_AGENT")); name != "" {
		return name
	}
	if os.Getenv("CLAUDECODE") == "1" {
		return "Claude Code"
	}
	return ""
}

// printBudget shows the user's spend this month when the server reports a
// budget, as a warning when it is over.
func printBudget(budget string, over bool, stdout io.Writer) {
	switch {
	case budget == "":
	case over:
		fmt.Fprintf(stdout, "Warning: %s Approvers see this too.\n", budget)
	default:
		fmt.Fprintln(stdout, budget)
	}
}

func (p *managedProvider) upCommand(request upRequest) []string {
	body := p.computeRequest(request)
	if body.PublicKey == "" {
		body.PublicKey = "<your ~/.ssh public key>"
	}
	data, _ := json.Marshal(body)
	return []string{"POST", p.url + "/v1/requests", string(data)}
}

// create sends the request and, unless --no-wait, waits for the decision
// and the start.
func (p *managedProvider) create(request upRequest, stdout, stderr io.Writer) error {
	var created serverRequest
	if err := p.call(http.MethodPost, "/v1/requests", p.computeRequest(request), &created); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Sent request %s to %s.\n", created.ID, p.url)
	printBudget(created.Budget, created.OverBudget, stdout)
	if created.State != "pending" && created.DecidedBy != "" {
		fmt.Fprintf(stdout, "Approved by %s.\n", created.DecidedBy)
	}
	if request.noWait && created.State == "pending" {
		return exitStatusError{code: exitPending, message: fmt.Sprintf(
			"%s is waiting for approval; check it with `hi compute requests %s --wait`", created.ID, created.ID)}
	}
	_, err := p.waitForDecision(created.ID, 0, stdout)
	return err
}

// waitForDecision follows a request until it runs or ends. With a timeout,
// a request still pending then returns the pending exit status.
func (p *managedProvider) waitForDecision(id string, timeout time.Duration, stdout io.Writer) (serverRequest, error) {
	var deadline time.Time
	if timeout > 0 {
		deadline = computeNow().Add(timeout)
	}
	shown := ""
	for {
		var request serverRequest
		if err := p.call(http.MethodGet, "/v1/requests/"+url.PathEscape(id), nil, &request); err != nil {
			return request, err
		}
		line := requestStateLine(request)
		if line != shown {
			fmt.Fprintln(stdout, line)
			shown = line
		}
		switch request.State {
		case "running", "stopped", "approved":
			return request, nil
		case "denied":
			message := fmt.Sprintf("%s was denied by %s", id, request.DecidedBy)
			if request.DenyReason != "" {
				message += ": " + request.DenyReason
			}
			return request, exitStatusError{code: exitDenied, message: message}
		case "expired":
			return request, fmt.Errorf("%s expired without a decision; send it again if you still need it", id)
		case "failed":
			return request, fmt.Errorf("%s was approved but could not start: %s", id, request.Error)
		}
		if !deadline.IsZero() && computeNow().After(deadline) {
			return request, exitStatusError{code: exitPending, message: fmt.Sprintf(
				"%s is still %s; check again with `hi compute requests %s --wait`", id, request.State, id)}
		}
		time.Sleep(managedPollEvery)
	}
}

func requestStateLine(request serverRequest) string {
	switch request.State {
	case "pending":
		return "Waiting for approval… (Ctrl-C stops waiting; the request stays open)"
	case "approved":
		return fmt.Sprintf("Approved by %s: %s runs %s longer.", request.DecidedBy, request.Name,
			formatDuration(time.Duration(request.MaxSeconds)*time.Second))
	case "starting":
		line := fmt.Sprintf("Approved by %s; starting", request.DecidedBy)
		if request.Progress != "" {
			line += ": " + request.Progress
		}
		return line
	case "running":
		if request.Approved != "" {
			return fmt.Sprintf("%s is running on %s (%s); the approved %s was sold out.",
				request.Name, request.Hardware, request.Rate, request.Approved)
		}
		return fmt.Sprintf("%s is running.", request.Name)
	case "stopped":
		return fmt.Sprintf("%s has stopped.", request.Name)
	}
	return request.State
}

func (p *managedProvider) list() ([]computeInstance, error) {
	var leases []serverLease
	if err := p.call(http.MethodGet, "/v1/instances", nil, &leases); err != nil {
		return nil, err
	}
	instances := make([]computeInstance, len(leases))
	for i, lease := range leases {
		instances[i] = computeInstance{
			name:     lease.Name,
			hardware: lease.Hardware,
			detail:   fmt.Sprintf("started through %s (%s), %s", p.url, lease.Request, lease.Rate),
			state:    lease.State,
			managed:  true,
			created:  lease.Started,
			deadline: lease.Deadline,
		}
	}
	return instances, nil
}

func (p *managedProvider) stop(name string, stdout, _ io.Writer) error {
	if err := p.call(http.MethodPost, "/v1/instances/"+url.PathEscape(name)+"/stop", nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Stopped %s.\n", name)
	return nil
}

func (p *managedProvider) ssh(name string) (sshTarget, error) {
	var target apiSSH
	if err := p.call(http.MethodGet, "/v1/instances/"+url.PathEscape(name)+"/ssh", nil, &target); err != nil {
		return sshTarget{}, err
	}
	return sshTarget{options: target.Options, destination: target.Destination, hint: target.Hint}, nil
}

func (p *managedProvider) logs(name string, follow bool, lines int, stdin io.Reader, stdout, stderr io.Writer) error {
	return sshLogs(p, name, follow, lines, stdin, stdout, stderr)
}

// ---------------------------------------------------------------------------
// hi compute requests

func computeRequestsCommand(args []string, stdout, stderr io.Writer) error {
	flags := &flagSet{newComputeFlags("requests", stderr)}
	wait := flags.Bool("wait", false, "wait for the decision")
	timeout := &lifetimeFlag{value: noLimit}
	flags.Var(timeout, "timeout", "stop waiting after this long, e.g. 10m")
	asJSON := flags.Bool("json", false, "machine-readable output")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	if len(positional) > 1 || (*wait && len(positional) == 0) {
		return usageError{"usage: hi compute requests [<id> [--wait] [--timeout D]] [--json]"}
	}
	connection, err := loadServerConnection()
	if err != nil {
		return err
	}
	if connection == nil {
		return errors.New("not connected to a hi server; requests are only needed for managed compute")
	}
	provider := &managedProvider{url: connection.URL}

	if len(positional) == 0 {
		var requests []serverRequest
		if err := provider.call(http.MethodGet, "/v1/requests", nil, &requests); err != nil {
			return err
		}
		if *asJSON {
			return writeRequestsJSON(stdout, requests)
		}
		if len(requests) == 0 {
			fmt.Fprintln(stdout, "You have no requests.")
			return nil
		}
		table := newTable(stdout)
		fmt.Fprintln(table, "ID\tSTATE\tNAME\tHARDWARE\tMAX\tSENT")
		for _, request := range requests {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s/%s\t%s\t%s ago\n", request.ID, request.State, request.Name,
				request.Provider, request.Hardware, formatDuration(time.Duration(request.MaxSeconds)*time.Second),
				formatDuration(computeNow().Sub(request.Created)))
		}
		return table.Flush()
	}

	id := positional[0]
	var request serverRequest
	if *wait {
		output := stdout
		if *asJSON {
			output = io.Discard
		}
		request, err = provider.waitForDecision(id, timeout.value, output)
	} else if err = provider.call(http.MethodGet, "/v1/requests/"+url.PathEscape(id), nil, &request); err == nil {
		err = requestStatusError(request)
	}
	if *asJSON && request.ID != "" {
		if writeErr := writeRequestsJSON(stdout, request); writeErr != nil {
			return writeErr
		}
	} else if !*wait && request.ID != "" {
		printRequest(request, stdout)
	}
	if err == nil && *wait && request.State == "running" && !*asJSON {
		fmt.Fprintf(stdout, "\nNext:\n  hi compute ssh %s\n  hi compute tunnel %s 8000\n  hi compute stop %s\n",
			request.Name, request.Name, request.Name)
	}
	return err
}

// requestStatusError gives a looked-up request the same exit status as
// waiting for it: pending and denied have their own.
func requestStatusError(request serverRequest) error {
	switch request.State {
	case "pending", "starting":
		return exitStatusError{code: exitPending, message: fmt.Sprintf("%s is %s", request.ID, request.State)}
	case "denied":
		return exitStatusError{code: exitDenied, message: fmt.Sprintf("%s was denied", request.ID)}
	case "failed", "expired":
		return fmt.Errorf("%s %s", request.ID, request.State)
	}
	return nil
}

func printRequest(request serverRequest, stdout io.Writer) {
	fmt.Fprintf(stdout, "Request:   %s\n", request.ID)
	fmt.Fprintf(stdout, "State:     %s\n", request.State)
	fmt.Fprintf(stdout, "Instance:  %s/%s on %s (%s)\n", request.Provider, request.Name, request.Hardware, request.Rate)
	if request.Approved != "" {
		fmt.Fprintf(stdout, "Approved:  %s, sold out; replaced within the approval's price bound\n", request.Approved)
	}
	fmt.Fprintf(stdout, "Max:       %s\n", formatDuration(time.Duration(request.MaxSeconds)*time.Second))
	fmt.Fprintf(stdout, "Reason:    %s\n", request.Reason)
	if request.DecidedBy != "" {
		fmt.Fprintf(stdout, "Decided:   by %s at %s\n", request.DecidedBy, request.DecidedAt.Local().Format("15:04"))
	}
	if request.DenyReason != "" {
		fmt.Fprintf(stdout, "Denied:    %s\n", request.DenyReason)
	}
	if request.Error != "" {
		fmt.Fprintf(stdout, "Error:     %s\n", request.Error)
	}
}

func writeRequestsJSON(stdout io.Writer, value any) error {
	scrub := func(request serverRequest) serverRequest {
		request.PublicKey = ""
		return request
	}
	switch v := value.(type) {
	case serverRequest:
		value = scrub(v)
	case []serverRequest:
		for i := range v {
			v[i] = scrub(v[i])
		}
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// ---------------------------------------------------------------------------
// hi compute extend

func computeExtendCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := &flagSet{newComputeFlags("extend", stderr)}
	reason := flags.String("reason", "", "why it needs longer; approvers see it")
	noWait := flags.Bool("no-wait", false, "return while approval is pending")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	if len(positional) != 2 {
		return usageError{"usage: hi compute extend <name> <duration> [--reason R] [--no-wait]"}
	}
	extra, err := parseLifetime(positional[1])
	if err != nil || extra == noLimit {
		return usageError{fmt.Sprintf("invalid duration %q; use hours such as 1 or 1.5, or 30m", positional[1])}
	}
	provider, name, err := findInstance(positional[0])
	if err != nil {
		return err
	}
	managed, ok := provider.(*managedProvider)
	if !ok {
		return usageError{fmt.Sprintf("%s is not managed by a hi server; its limit was set when it started", name)}
	}
	*reason = strings.TrimSpace(*reason)
	if *reason == "" {
		if !isTerminal(stdin) {
			return usageError{"approvers need a reason; add --reason \"why it needs longer\""}
		}
		fmt.Fprint(stdout, "Why does it need longer? (approvers see this) ")
		line, err := readLine(stdin)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if *reason = strings.TrimSpace(line); *reason == "" {
			return errors.New("cancelled; approvers need a reason")
		}
	}
	var created serverRequest
	body := apiExtend{Seconds: int64(extra / time.Second), Reason: *reason, Agent: agentName()}
	if err := managed.call(http.MethodPost, "/v1/instances/"+url.PathEscape(name)+"/extend", body, &created); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Asked %s for %s more on %s (request %s).\n", managed.url, formatDuration(extra), name, created.ID)
	printBudget(created.Budget, created.OverBudget, stdout)
	if created.State != "pending" && created.DecidedBy != "" {
		fmt.Fprintf(stdout, "Approved by %s.\n", created.DecidedBy)
	}
	if *noWait && created.State == "pending" {
		return exitStatusError{code: exitPending, message: fmt.Sprintf(
			"%s is waiting for approval; check it with `hi compute requests %s --wait`", created.ID, created.ID)}
	}
	_, err = managed.waitForDecision(created.ID, 0, stdout)
	return err
}
