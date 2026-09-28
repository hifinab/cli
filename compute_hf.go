package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

const (
	hfDefaultEndpoint = "https://huggingface.co"
	hfCPUImage        = "python:3.12"
	hfGPUImage        = "pytorch/pytorch:2.6.0-cuda12.4-cudnn9-devel"
	hfUVImage         = "ghcr.io/astral-sh/uv:python3.12-bookworm"
	hfServeImage      = "ghcr.io/ggml-org/llama.cpp:server-cuda"
	hfMaxScriptBytes  = 96 << 10
	hfRequestTimeout  = 30 * time.Second
	// Machines above this hourly price ask for confirmation.
	hfConfirmAboveUSD = 0.10
)

// hfPollEvery is shortened in tests.
var hfPollEvery = 5 * time.Second

// hfProvider runs Hugging Face Jobs through the documented Jobs REST API
// (https://huggingface.co/api/jobs); the hf CLI is only used to sign in.
type hfProvider struct {
	mu       sync.Mutex
	signedIn *hfAccount
	flavors  []hfFlavor
}

// hfAccount is the signed-in user and the organizations they belong to.
// canPay says whether an account can be billed for Jobs; the API does not
// report the credit balance itself.
type hfAccount struct {
	Name   string `json:"name"`
	CanPay bool   `json:"canPay"`
	Orgs   []struct {
		Name      string `json:"name"`
		CanPay    bool   `json:"canPay"`
		Plan      string `json:"plan"`
		RoleInOrg string `json:"roleInOrg"`
	} `json:"orgs"`
}

type hfFlavor struct {
	Name        string  `json:"name"`
	PrettyName  string  `json:"prettyName"`
	CPU         string  `json:"cpu"`
	RAM         string  `json:"ram"`
	UnitCostUSD float64 `json:"unitCostUSD"`
	UnitLabel   string  `json:"unitLabel"`
	Accelerator *struct {
		Type     string `json:"type"`
		Model    string `json:"model"`
		Quantity string `json:"quantity"`
		VRAM     string `json:"vram"`
	} `json:"accelerator"`
}

type hfJob struct {
	ID        string            `json:"id"`
	CreatedAt string            `json:"createdAt"`
	Flavor    string            `json:"flavor"`
	Timeout   int               `json:"timeout"`
	Labels    map[string]string `json:"labels"`
	Owner     struct {
		Name string `json:"name"`
	} `json:"owner"`
	Status struct {
		Stage      string   `json:"stage"`
		Message    string   `json:"message"`
		SSHURL     string   `json:"sshUrl"`
		ExposeURLs []string `json:"exposeUrls"`
	} `json:"status"`
}

func newHFProvider() *hfProvider { return &hfProvider{} }

func (*hfProvider) name() string { return "hf" }

// Jobs take a timeout, so the provider enforces --max itself.
func (*hfProvider) enforcesLifetime() bool { return true }

func (*hfProvider) maxLifetime() time.Duration { return 0 }

func (*hfProvider) reservedPorts() map[int]string { return nil }

func (*hfProvider) account() (string, error) { return "", nil }

func hfEndpoint() string {
	if endpoint := strings.TrimRight(os.Getenv("HF_ENDPOINT"), "/"); endpoint != "" {
		return endpoint
	}
	return hfDefaultEndpoint
}

// hfToken finds the token the way huggingface_hub does: HF_TOKEN, then the
// file at HF_TOKEN_PATH, then $HF_HOME/token. It is held in memory only.
func hfToken() string {
	if token := strings.TrimSpace(os.Getenv("HF_TOKEN")); token != "" {
		return token
	}
	path := os.Getenv("HF_TOKEN_PATH")
	if path == "" {
		home := os.Getenv("HF_HOME")
		if home == "" {
			base := os.Getenv("XDG_CACHE_HOME")
			if base == "" {
				userHome, err := os.UserHomeDir()
				if err != nil {
					return ""
				}
				base = filepath.Join(userHome, ".cache")
			}
			home = filepath.Join(base, "huggingface")
		}
		path = filepath.Join(home, "token")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (*hfProvider) check() providerStatus {
	if hfToken() == "" {
		return providerStatus{installed: true, hints: []string{"sign in with `hi login hf`"}}
	}
	return providerStatus{installed: true, signedIn: true}
}

// request sends one API call. Errors name the problem without the token.
func (p *hfProvider) request(method, path string, body, result any) error {
	token := hfToken()
	if token == "" {
		return errors.New("not signed in to Hugging Face; run `hi login hf`")
	}
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), hfRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, hfEndpoint()+path, payload)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "hi/"+version)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("Hugging Face API: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode >= 300 {
		return p.apiError(response.StatusCode, data)
	}
	if result == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, result)
}

func (p *hfProvider) apiError(status int, data []byte) error {
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(data, &body)
	message := strings.TrimSpace(body.Error)
	if message == "" {
		message = strings.TrimSpace(string(data))
		if len(message) > 200 {
			message = message[:200] + "..."
		}
	}
	switch status {
	case http.StatusUnauthorized:
		return errors.New("Hugging Face rejected the token; run `hi login hf` again")
	case http.StatusPaymentRequired:
		return fmt.Errorf("Hugging Face Jobs need pre-paid credits: %s\n"+
			"Add credits at https://huggingface.co/settings/billing, or bill an organization that can pay:\n"+
			"see `hi compute billing`, then `hi compute billing ORG`", message)
	case http.StatusForbidden:
		return fmt.Errorf("Hugging Face refused: %s (check --namespace and the token's permissions)", message)
	}
	return fmt.Errorf("Hugging Face API returned %d: %s", status, message)
}

func (p *hfProvider) whoamiAccount() (*hfAccount, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.signedIn != nil {
		return p.signedIn, nil
	}
	var account hfAccount
	if err := p.request(http.MethodGet, "/api/whoami-v2", nil, &account); err != nil {
		return nil, err
	}
	p.signedIn = &account
	return p.signedIn, nil
}

func (p *hfProvider) whoami() (string, error) {
	account, err := p.whoamiAccount()
	if err != nil {
		return "", err
	}
	return account.Name, nil
}

// defaultNamespace picks who pays: --namespace, then HI_HF_NAMESPACE, then
// the account saved with `hi compute billing`, then the user if they can pay,
// then their only organization that can pay, else the user.
func (p *hfProvider) defaultNamespace(requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	if configured := os.Getenv("HI_HF_NAMESPACE"); configured != "" {
		return configured, nil
	}
	if config, err := loadComputeConfig(); err == nil && config.HFNamespace != "" {
		return config.HFNamespace, nil
	}
	account, err := p.whoamiAccount()
	if err != nil {
		return "", err
	}
	if account.CanPay {
		return account.Name, nil
	}
	var paying []string
	for _, org := range account.Orgs {
		if org.CanPay {
			paying = append(paying, org.Name)
		}
	}
	if len(paying) == 1 {
		return paying[0], nil
	}
	return account.Name, nil
}

// billedTo names the account a new job would bill, for confirmations.
func (p *hfProvider) billedTo(requested string) string {
	namespace, err := p.defaultNamespace(requested)
	if err != nil {
		return ""
	}
	return namespace
}

func (p *hfProvider) hardware() ([]computeHardware, error) {
	p.mu.Lock()
	cached := p.flavors
	p.mu.Unlock()
	if cached == nil {
		var flavors []hfFlavor
		if err := p.request(http.MethodGet, "/api/jobs/hardware", nil, &flavors); err != nil {
			return nil, err
		}
		p.mu.Lock()
		p.flavors = flavors
		p.mu.Unlock()
		cached = flavors
	}
	options := make([]computeHardware, 0, len(cached))
	for _, flavor := range cached {
		hourly := flavor.UnitCostUSD
		if flavor.UnitLabel == "minute" {
			hourly *= 60
		}
		option := computeHardware{
			name:   flavor.Name,
			kind:   "CPU",
			memory: flavor.RAM + " RAM",
			rate:   fmt.Sprintf("$%.2f/h", hourly),
			paid:   hourly > hfConfirmAboveUSD,
		}
		if flavor.Accelerator != nil {
			option.kind = strings.ToUpper(flavor.Accelerator.Type)
			option.memory = flavor.Accelerator.VRAM + " VRAM"
			if flavor.Accelerator.Quantity != "" && flavor.Accelerator.Quantity != "1" {
				option.memory = flavor.Accelerator.Quantity + "x, " + option.memory
			}
		}
		options = append(options, option)
	}
	return options, nil
}

func (p *hfProvider) validateRun(request runRequest) error {
	if request.highMem {
		return usageError{"--high-mem applies to Colab only; choose a larger --gpu flavor instead"}
	}
	for _, secret := range request.secrets {
		if _, ok := os.LookupEnv(secret); !ok {
			return usageError{fmt.Sprintf("secret %s is not set in this shell; export it first", secret)}
		}
	}
	if request.script != "" {
		info, err := os.Stat(request.script)
		if err != nil {
			return err
		}
		if info.Size() > hfMaxScriptBytes {
			return usageError{fmt.Sprintf("%s is larger than %d KB; publish it at a URL or bake it into an image",
				request.script, hfMaxScriptBytes>>10)}
		}
	}
	return nil
}

func hfLabels(name string) map[string]string {
	return map[string]string{"name": name, "managed-by": "hi"}
}

// hfNoLimit is the timeout sent for "no limit": Jobs default to 30 minutes
// without one, and accept a year.
const hfNoLimit = 365 * 24 * time.Hour

func hfTimeout(lifetime time.Duration) int {
	if lifetime == noLimit {
		return int(hfNoLimit.Seconds())
	}
	return int(lifetime.Seconds())
}

func environmentMap(entries []string) map[string]string {
	env := map[string]string{}
	for _, entry := range entries {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	return env
}

// jobSpec builds the request body for POST /api/jobs/{namespace}.
func (p *hfProvider) jobSpec(image string, command []string, env map[string]string, secrets []string,
	flavor string, lifetime time.Duration, name string, expose []int, ssh bool) map[string]any {
	spec := map[string]any{
		"dockerImage":    image,
		"command":        command,
		"arguments":      []string{},
		"environment":    env,
		"flavor":         flavor,
		"timeoutSeconds": hfTimeout(lifetime),
	}
	if name != "" {
		spec["labels"] = hfLabels(name)
	}
	if len(secrets) > 0 {
		values := map[string]string{}
		for _, secret := range secrets {
			values[secret] = os.Getenv(secret)
		}
		spec["secrets"] = values
	}
	if len(expose) > 0 {
		spec["expose"] = map[string]any{"ports": expose}
	}
	if ssh {
		spec["ssh"] = map[string]bool{"enabled": true}
	}
	return spec
}

// embeddedScriptCommand runs a script shipped base64-encoded in the job's
// environment, so no upload is needed.
func embeddedScriptCommand(runner, file string, env map[string]string, script []byte) []string {
	env["HI_SCRIPT_B64"] = base64.StdEncoding.EncodeToString(script)
	return []string{"bash", "-c",
		fmt.Sprintf(`echo "$HI_SCRIPT_B64" | base64 -d > /tmp/%s && unset HI_SCRIPT_B64 && exec %s /tmp/%s "$@"`,
			file, runner, file), "hi"}
}

func (p *hfProvider) runSpec(request runRequest) (map[string]any, error) {
	env := environmentMap(request.env)
	if request.image != "" {
		return p.jobSpec(request.image, request.command, env, request.secrets,
			request.hardware.name, request.max, request.name, nil, false), nil
	}
	script, err := os.ReadFile(request.script)
	if err != nil {
		return nil, err
	}
	command := embeddedScriptCommand("uv run", "script.py", env, script)
	command = append(command, request.args...)
	return p.jobSpec(hfUVImage, command, env, request.secrets,
		request.hardware.name, request.max, request.name, nil, false), nil
}

func (p *hfProvider) upSpec(request upRequest) map[string]any {
	env := map[string]string{}
	if request.serve != nil {
		recipe := request.serve
		for key, value := range map[string]string{
			"REPO": recipe.repo, "QUANT": recipe.quant, "CTX": strconv.Itoa(recipe.context),
			"PORT": strconv.Itoa(serveRemotePort), "HOST": "0.0.0.0", "ALIAS": recipe.alias,
			"EXTRA_ARGS": recipe.args, "HI_FOREGROUND": "1",
		} {
			env[key] = value
		}
		command := embeddedScriptCommand("bash", "serve.sh", env, serveLlamaCppScript)
		return p.jobSpec(hfServeImage, command, env, nil, request.hardware.name, request.max,
			request.name, []int{serveRemotePort}, true)
	}
	image := request.image
	if image == "" {
		image = hfCPUImage
		if request.hardware.kind == "GPU" {
			image = hfGPUImage
		}
	}
	return p.jobSpec(image, []string{"sleep", "infinity"}, env, nil, request.hardware.name, request.max,
		request.name, nil, true)
}

func describeSpec(namespace string, spec map[string]any) []string {
	shown := map[string]any{}
	for key, value := range spec {
		shown[key] = value
	}
	if env, ok := spec["environment"].(map[string]string); ok {
		copied := map[string]string{}
		for key, value := range env {
			if key == "HI_SCRIPT_B64" {
				value = fmt.Sprintf("<%d bytes of script>", len(value))
			}
			copied[key] = value
		}
		shown["environment"] = copied
	}
	if secrets, ok := spec["secrets"].(map[string]string); ok {
		names := map[string]string{}
		for key := range secrets {
			names[key] = "<from your environment>"
		}
		shown["secrets"] = names
	}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(shown)
	if namespace == "" {
		namespace = "<your account>"
	}
	return []string{"POST", hfEndpoint() + "/api/jobs/" + namespace, strings.TrimSpace(data.String())}
}

func (p *hfProvider) upCommand(request upRequest) []string {
	return describeSpec(request.namespace, p.upSpec(request))
}

func (p *hfProvider) runCommand(request runRequest) []string {
	spec, err := p.runSpec(request)
	if err != nil {
		return []string{"(could not build the request: " + err.Error() + ")"}
	}
	return describeSpec(request.namespace, spec)
}

func (p *hfProvider) createJob(namespace string, spec map[string]any) (hfJob, error) {
	var job hfJob
	err := p.request(http.MethodPost, "/api/jobs/"+url.PathEscape(namespace), spec, &job)
	return job, err
}

func (p *hfProvider) create(request upRequest, stdout, stderr io.Writer) error {
	namespace, err := p.defaultNamespace(request.namespace)
	if err != nil {
		return err
	}
	job, err := p.createJob(namespace, p.upSpec(request))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Started job %s: %s/jobs/%s/%s\n", job.ID, hfEndpoint(), namespace, job.ID)
	return nil
}

// namespaces are the accounts whose jobs hi lists: the user, HI_HF_NAMESPACE,
// and any namespace hi started an instance in.
func (p *hfProvider) namespaces() ([]string, error) {
	user, err := p.whoami()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{user: true}
	result := []string{user}
	add := func(namespace string) {
		if namespace != "" && !seen[namespace] {
			seen[namespace] = true
			result = append(result, namespace)
		}
	}
	add(os.Getenv("HI_HF_NAMESPACE"))
	if billed, err := p.defaultNamespace(""); err == nil {
		add(billed)
	}
	if records, err := loadComputeRecords(); err == nil {
		for _, record := range records {
			if record.Provider == "hf" {
				add(record.Namespace)
			}
		}
	}
	return result, nil
}

func hfJobActive(stage string) bool {
	switch stage {
	case "COMPLETED", "CANCELED", "ERROR", "DELETED":
		return false
	}
	return true
}

// jobName is the hi name from the job's label, or its ID.
func jobName(job hfJob) string {
	if name := job.Labels["name"]; validLookupName(name) {
		return name
	}
	return job.ID
}

func (p *hfProvider) activeJobs() ([]hfJob, error) {
	namespaces, err := p.namespaces()
	if err != nil {
		return nil, err
	}
	var active []hfJob
	for _, namespace := range namespaces {
		var jobs []hfJob
		if err := p.request(http.MethodGet, "/api/jobs/"+url.PathEscape(namespace), nil, &jobs); err != nil {
			return nil, err
		}
		for _, job := range jobs {
			if hfJobActive(job.Status.Stage) {
				if job.Owner.Name == "" {
					job.Owner.Name = namespace
				}
				active = append(active, job)
			}
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].CreatedAt > active[j].CreatedAt })
	return active, nil
}

func (p *hfProvider) list() ([]computeInstance, error) {
	jobs, err := p.activeJobs()
	if err != nil {
		return nil, err
	}
	instances := make([]computeInstance, 0, len(jobs))
	for _, job := range jobs {
		instances = append(instances, hfInstance(job))
	}
	return instances, nil
}

func hfInstance(job hfJob) computeInstance {
	instance := computeInstance{
		name:     jobName(job),
		hardware: job.Flavor,
		detail:   fmt.Sprintf("%s/jobs/%s/%s", hfEndpoint(), job.Owner.Name, job.ID),
		state:    strings.ToLower(job.Status.Stage),
		managed:  job.Labels["managed-by"] == "hi",
	}
	if created, err := time.Parse(time.RFC3339, job.CreatedAt); err == nil {
		instance.created = created
		if job.Timeout > 0 && job.Timeout < int(hfNoLimit.Seconds()) && hfJobActive(job.Status.Stage) {
			instance.deadline = created.Add(time.Duration(job.Timeout) * time.Second)
		}
	}
	if job.Status.Message != "" {
		instance.state += ": " + job.Status.Message
	}
	return instance
}

func (p *hfProvider) lookup(name string) (computeInstance, error) {
	job, err := p.findJob(name)
	if err != nil {
		return computeInstance{}, err
	}
	return hfInstance(job), nil
}

// findJob resolves a hi name or job ID. Active jobs are checked first, then
// the namespace's history for finished jobs (useful for logs and wait).
func (p *hfProvider) findJob(name string) (hfJob, error) {
	jobs, err := p.activeJobs()
	if err != nil {
		return hfJob{}, err
	}
	for _, job := range jobs {
		if jobName(job) == name || job.ID == name {
			return job, nil
		}
	}
	namespaces, err := p.namespaces()
	if err != nil {
		return hfJob{}, err
	}
	for _, namespace := range namespaces {
		var history []hfJob
		if err := p.request(http.MethodGet, "/api/jobs/"+url.PathEscape(namespace), nil, &history); err != nil {
			return hfJob{}, err
		}
		sort.Slice(history, func(i, j int) bool { return history[i].CreatedAt > history[j].CreatedAt })
		for _, job := range history {
			if jobName(job) == name || job.ID == name {
				if job.Owner.Name == "" {
					job.Owner.Name = namespace
				}
				return job, nil
			}
		}
	}
	return hfJob{}, fmt.Errorf("no Hugging Face job named %q", name)
}

func (p *hfProvider) getJob(job hfJob) (hfJob, error) {
	var current hfJob
	err := p.request(http.MethodGet, "/api/jobs/"+url.PathEscape(job.Owner.Name)+"/"+url.PathEscape(job.ID), nil, &current)
	if current.Owner.Name == "" {
		current.Owner.Name = job.Owner.Name
	}
	return current, err
}

func (p *hfProvider) stop(name string, stdout, stderr io.Writer) error {
	job, err := p.findJob(name)
	if err != nil {
		return err
	}
	path := "/api/jobs/" + url.PathEscape(job.Owner.Name) + "/" + url.PathEscape(job.ID) + "/cancel"
	if err := p.request(http.MethodPost, path, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Stopped %s.\n", name)
	return nil
}

// ssh waits for the job to run, then connects to the Jobs SSH gateway. It
// needs an SSH public key registered at https://huggingface.co/settings/keys.
func (p *hfProvider) ssh(name string) (sshTarget, error) {
	job, err := p.findJob(name)
	if err != nil {
		return sshTarget{}, err
	}
	deadline := time.Now().Add(10 * time.Minute)
	for job.Status.SSHURL == "" || job.Status.Stage != "RUNNING" {
		if !hfJobActive(job.Status.Stage) {
			return sshTarget{}, fmt.Errorf("%s has finished (%s)", name, strings.ToLower(job.Status.Stage))
		}
		if time.Now().After(deadline) {
			return sshTarget{}, fmt.Errorf("%s did not become reachable over SSH; was it started by hi?", name)
		}
		time.Sleep(hfPollEvery)
		if job, err = p.getJob(job); err != nil {
			return sshTarget{}, err
		}
	}
	address, err := url.Parse(job.Status.SSHURL)
	if err != nil || address.User == nil || address.Hostname() == "" {
		return sshTarget{}, fmt.Errorf("unexpected SSH address %q", job.Status.SSHURL)
	}
	options := []string{
		"-o", "ServerAliveInterval=30",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if port := address.Port(); port != "" {
		options = append(options, "-p", port)
	}
	return sshTarget{
		options:     options,
		destination: address.User.Username() + "@" + address.Hostname(),
		hint: "hi: Hugging Face only accepts SSH keys registered on your account; add your public key " +
			"(~/.ssh/id_ed25519.pub) at https://huggingface.co/settings/keys",
	}, nil
}

// streamLogs prints the job's logs from the Server-Sent Events endpoint. With
// follow it reconnects until the job finishes; without, it stops once the
// history has been replayed.
func (p *hfProvider) streamLogs(job hfJob, follow bool, tail int, stdout io.Writer) error {
	path := "/api/jobs/" + url.PathEscape(job.Owner.Name) + "/" + url.PathEscape(job.ID) + "/logs"
	if tail > 0 {
		path += "?tail=" + strconv.Itoa(tail)
	}
	seen := 0
	for {
		idle := 5 * time.Second
		if follow {
			idle = 2 * time.Minute
		}
		count, err := p.readEvents(path, idle, func(line string) {
			if strings.HasPrefix(line, "===== Job started") {
				return
			}
			fmt.Fprintln(stdout, line)
		}, seen)
		seen += count
		if !follow {
			return err
		}
		current, statusErr := p.getJob(job)
		if statusErr == nil && !hfJobActive(current.Status.Stage) {
			return nil
		}
		time.Sleep(hfPollEvery)
	}
}

// readEvents reads `data: {...}` events, skipping the first `skip` already
// printed ones, and returns when the stream ends or stays idle.
func (p *hfProvider) readEvents(path string, idle time.Duration, emit func(string), skip int) (int, error) {
	token := hfToken()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, hfEndpoint()+path, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return 0, p.apiError(response.StatusCode, data)
	}

	lines := make(chan string)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 64<<10), 4<<20)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	index := 0
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return max(index-skip, 0), nil
			}
			data, isEvent := strings.CutPrefix(line, "data: ")
			if !isEvent || !strings.HasPrefix(data, "{") {
				continue
			}
			var event struct {
				Data string `json:"data"`
			}
			if json.Unmarshal([]byte(data), &event) != nil {
				continue
			}
			if index >= skip {
				emit(event.Data)
			}
			index++
			timer.Reset(idle)
		case <-timer.C:
			return max(index-skip, 0), nil
		}
	}
}

func (p *hfProvider) logs(name string, follow bool, lines int, stdin io.Reader, stdout, stderr io.Writer) error {
	job, err := p.findJob(name)
	if err != nil {
		return err
	}
	return p.streamLogs(job, follow, lines, stdout)
}

var hfExitCodePattern = regexp.MustCompile(`exit code: (\d+)`)

// hfExitCode passes a failed job's own exit code through when Hugging Face
// reports it, as in "Job failed with exit code: 3. Reason: Error."
func hfExitCode(job hfJob) int {
	switch job.Status.Stage {
	case "COMPLETED":
		return 0
	case "CANCELED":
		return 130
	}
	if match := hfExitCodePattern.FindStringSubmatch(job.Status.Message); match != nil {
		if code, err := strconv.Atoi(match[1]); err == nil && code > 0 && code < 256 {
			return code
		}
	}
	return 1
}

func (p *hfProvider) waitForJob(job hfJob, stdout io.Writer) (int, error) {
	for {
		current, err := p.getJob(job)
		if err != nil {
			return 0, err
		}
		if !hfJobActive(current.Status.Stage) {
			message := strings.ToLower(current.Status.Stage)
			if current.Status.Message != "" {
				message += ": " + current.Status.Message
			}
			fmt.Fprintf(stdout, "%s %s.\n", jobName(current), message)
			return hfExitCode(current), nil
		}
		time.Sleep(hfPollEvery)
	}
}

func (p *hfProvider) wait(name string, stdout, stderr io.Writer) (int, error) {
	job, err := p.findJob(name)
	if err != nil {
		return 0, err
	}
	return p.waitForJob(job, stdout)
}

func (p *hfProvider) runJob(request runRequest, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	namespace, err := p.defaultNamespace(request.namespace)
	if err != nil {
		return 0, err
	}
	spec, err := p.runSpec(request)
	if err != nil {
		return 0, err
	}
	job, err := p.createJob(namespace, spec)
	if err != nil {
		return 0, err
	}
	if job.Owner.Name == "" {
		job.Owner.Name = namespace
	}
	fmt.Fprintf(stdout, "Started job %s: %s/jobs/%s/%s\n", job.ID, hfEndpoint(), namespace, job.ID)
	if request.detach {
		handle := "hf/" + job.ID
		if request.name != "" {
			handle = request.name
		}
		fmt.Fprintf(stdout, "Follow it with: hi compute logs %s --follow\n", handle)
		fmt.Fprintf(stdout, "Wait for it:    hi compute wait %s\n", handle)
		return 0, nil
	}
	if err := p.streamLogs(job, true, 0, stdout); err != nil {
		fmt.Fprintf(stderr, "hi: log stream ended: %v\n", err)
	}
	return p.waitForJob(job, stdout)
}

// serverState reads the last `hi-state:` line the serve script printed.
func (p *hfProvider) serverState(name string) (string, error) {
	job, err := p.findJob(name)
	if err != nil {
		return "", err
	}
	if !hfJobActive(job.Status.Stage) {
		message := "failed: the job ended (" + strings.ToLower(job.Status.Stage) + ")"
		if job.Status.Message != "" {
			message += ": " + strings.TrimSuffix(job.Status.Message, ".")
		}
		return message, nil
	}
	if job.Status.Stage != "RUNNING" {
		return "waiting for hardware (" + strings.ToLower(job.Status.Stage) + ")", nil
	}
	state := "starting"
	path := "/api/jobs/" + url.PathEscape(job.Owner.Name) + "/" + url.PathEscape(job.ID) + "/logs?tail=200"
	_, err = p.readEvents(path, 3*time.Second, func(line string) {
		if value, ok := strings.CutPrefix(line, "hi-state: "); ok {
			state = value
		}
	}, 0)
	return state, err
}

func (p *hfProvider) serverURL(name string) (string, error) {
	job, err := p.findJob(name)
	if err != nil {
		return "", err
	}
	if len(job.Status.ExposeURLs) == 0 {
		return "", fmt.Errorf("%s exposes no port", name)
	}
	return strings.TrimRight(job.Status.ExposeURLs[0], "/"), nil
}

// billing prints the user's accounts, or saves which one to bill.
func (p *hfProvider) billing(choice string, clear bool, stdout io.Writer) error {
	account, err := p.whoamiAccount()
	if err != nil {
		return err
	}
	if clear {
		if err := saveHFNamespace(""); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Hugging Face billing is automatic again.")
		choice = ""
	} else if choice != "" {
		known, canPay := choice == account.Name, account.CanPay
		for _, org := range account.Orgs {
			if org.Name == choice {
				known, canPay = true, org.CanPay
			}
		}
		if !known {
			return fmt.Errorf("%s is not your account or one of your organizations; see `hi compute billing`", choice)
		}
		if err := saveHFNamespace(choice); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Hugging Face jobs now bill %s.\n", choice)
		if !canPay {
			fmt.Fprintf(stdout, "Note: %s cannot pay for Jobs yet; add credits at https://huggingface.co/settings/billing\n", choice)
		}
		return nil
	}

	billed, err := p.defaultNamespace("")
	if err != nil {
		return err
	}
	table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "ACCOUNT\tTYPE\tCAN PAY\tPLAN\tBILLED")
	row := func(name, kind string, canPay bool, plan string) {
		pays, mark := "no", ""
		if canPay {
			pays = "yes"
		}
		if name == billed {
			mark = "<- hi bills this"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", name, kind, pays, plan, mark)
	}
	row(account.Name, "user", account.CanPay, "")
	for _, org := range account.Orgs {
		row(org.Name, "org", org.CanPay, org.Plan)
	}
	table.Flush()
	fmt.Fprintf(stdout, "\nChosen by: %s\n", p.billingReason())
	fmt.Fprintln(stdout, "Change with `hi compute billing ACCOUNT`; `--clear` makes it automatic again.")
	return nil
}

func (p *hfProvider) billingReason() string {
	if os.Getenv("HI_HF_NAMESPACE") != "" {
		return "HI_HF_NAMESPACE in your environment"
	}
	if config, err := loadComputeConfig(); err == nil && config.HFNamespace != "" {
		return "`hi compute billing` (saved in " + computeConfigPath() + ")"
	}
	return "automatic: your own account if it can pay, else your only organization that can"
}
