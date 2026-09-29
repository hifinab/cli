package main

import (
	"bytes"
	"context"
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
	"time"
)

const (
	runpodImage        = "runpod/pytorch:1.0.2-cu1281-torch280-ubuntu2404"
	runpodKeysURL      = "https://console.runpod.io/user/credentials"
	runpodCPUVcpus     = 2
	runpodReadyTimeout = 15 * time.Minute
)

// These are replaced in tests.
var (
	runpodAPIBase   = "https://api.runpod.io/v2"
	runpodPollEvery = 5 * time.Second
)

// runpodProvider drives RunPod Pods through the REST API v2
// (https://api.runpod.io/v2; v1 retires on 2026-11-15). Pods have no native
// time limit, so hi runs its local watcher and also installs a watchdog on
// the pod that terminates it at --max.
type runpodProvider struct {
	mu      sync.Mutex
	options []runpodOption
}

// runpodOption is a hardware choice: a short hi name for a RunPod GPU or CPU
// type, which RunPod names like "NVIDIA GeForce RTX 4090".
type runpodOption struct {
	hardware  computeHardware
	hourly    float64
	gpuID     string
	cpuID     string
	memoryGB  float64
	available bool
}

type runpodPod struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Status    string            `json:"status"`
	Cost      float64           `json:"cost"`
	CreatedAt string            `json:"createdAt"`
	Env       map[string]string `json:"env"`
	GPU       *struct {
		ID    string `json:"id"`
		Count int    `json:"count"`
	} `json:"gpu"`
	CPU *struct {
		ID string `json:"id"`
	} `json:"cpu"`
	SSH struct {
		Direct *struct {
			Host     string `json:"host"`
			Port     int    `json:"port"`
			Username string `json:"username"`
		} `json:"direct"`
	} `json:"ssh"`
}

func newRunpodProvider() *runpodProvider { return &runpodProvider{} }

func (*runpodProvider) name() string { return "runpod" }

func (*runpodProvider) maxLifetime() time.Duration { return 0 }

// RunPod has no native time limit; the local watcher and the pod watchdog
// enforce --max.
func (*runpodProvider) enforcesLifetime() bool { return false }

func (*runpodProvider) reservedPorts() map[int]string { return nil }

func (*runpodProvider) account() (string, error) { return "", nil }

// runpodToken finds the API key like runpodctl does: RUNPOD_API_KEY, then
// apiKey in ~/.runpod/config.toml. It is held in memory only.
func runpodToken() string {
	if token := strings.TrimSpace(os.Getenv("RUNPOD_API_KEY")); token != "" {
		return token
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".runpod", "config.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found && strings.TrimSpace(key) == "apiKey" {
			return strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return ""
}

func runpodConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".runpod", "config.toml"), nil
}

// signInRunpod checks an API key against RunPod and saves it where
// runpodctl keeps it, so both tools share one key. RunPod has no sign-in
// tool of its own for hi to delegate to.
func (p *runpodProvider) signIn(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("the API key is empty")
	}
	if strings.ContainsAny(key, "\"\n\r ") {
		return "", errors.New("that does not look like a RunPod API key")
	}
	if err := runpodRequest(key, http.MethodGet, "/catalog/cpus", nil, nil); err != nil {
		if strings.Contains(err.Error(), "rejected the API key") {
			return "", errors.New("RunPod rejected this key; check you copied all of it")
		}
		return "", err
	}
	path, err := runpodConfigPath()
	if err != nil {
		return "", err
	}
	var lines []string
	replaced := false
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if name, _, found := strings.Cut(line, "="); found && strings.TrimSpace(name) == "apiKey" {
				line = fmt.Sprintf("apiKey = %q", key)
				replaced = true
			}
			lines = append(lines, line)
		}
	}
	if !replaced {
		lines = append(lines, fmt.Sprintf("apiKey = %q", key))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	p.mu.Lock()
	p.options = nil
	p.mu.Unlock()
	return path, nil
}

func (*runpodProvider) check() providerStatus {
	if runpodToken() == "" {
		return providerStatus{installed: true, hints: []string{"sign in with `hi login runpod`"}}
	}
	status := providerStatus{installed: true, signedIn: true}
	if runpodPublicKey() == "" {
		status.hints = append(status.hints, "ssh and tunnels need a key: run `ssh-keygen -t ed25519`")
	}
	return status
}

func runpodPublicKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, name := range []string{"id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub"} {
		if data, err := os.ReadFile(filepath.Join(home, ".ssh", name)); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return ""
}

func (p *runpodProvider) request(method, path string, body, result any) error {
	token := runpodToken()
	if token == "" {
		return errors.New("not signed in to RunPod; run `hi login runpod`")
	}
	return runpodRequest(token, method, path, body, result)
}

func runpodRequest(token, method, path string, body, result any) error {
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
	request, err := http.NewRequestWithContext(ctx, method, runpodAPIBase+path, payload)
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
		return fmt.Errorf("RunPod API: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode >= 300 {
		return runpodError(response.StatusCode, data)
	}
	if result == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, result)
}

// runpodError explains RunPod's problem+json errors without the key.
func runpodError(status int, data []byte) error {
	var problem struct {
		Title   string `json:"title"`
		Detail  string `json:"detail"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &problem)
	detail := strings.TrimSpace(problem.Detail)
	if detail == "" {
		detail = strings.TrimSpace(problem.Title + " " + problem.Message)
	}
	if detail == "" {
		detail = strings.TrimSpace(string(data))
		if len(detail) > 200 {
			detail = detail[:200] + "..."
		}
	}
	switch status {
	case http.StatusUnauthorized:
		return errors.New("RunPod rejected the API key; run `hi login runpod` again")
	case http.StatusPaymentRequired:
		return fmt.Errorf("RunPod needs more account balance: %s\nAdd funds at https://console.runpod.io/user/billing", detail)
	case http.StatusForbidden:
		return fmt.Errorf("RunPod refused: %s (the API key may be read-only)", detail)
	case http.StatusBadRequest:
		return fmt.Errorf("RunPod could not start it: %s\nThis usually means none of that hardware is free right now; try another --gpu", detail)
	case http.StatusTooManyRequests:
		return errors.New("RunPod is rate limiting requests; try again in a minute")
	}
	return fmt.Errorf("RunPod API returned %d: %s", status, detail)
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// runpodSlug makes a short hardware name, such as "rtx-4090" for
// "NVIDIA GeForce RTX 4090".
func runpodSlug(name string) string {
	name = strings.ToLower(name)
	for _, prefix := range []string{"nvidia ", "geforce ", "amd ", "radeon "} {
		name = strings.ReplaceAll(name, prefix, "")
	}
	return strings.Trim(nonSlug.ReplaceAllString(name, "-"), "-")
}

func (p *runpodProvider) hardwareOptions() ([]runpodOption, error) {
	p.mu.Lock()
	cached := p.options
	p.mu.Unlock()
	if cached != nil {
		return cached, nil
	}

	var gpus struct {
		GPUs []struct {
			ID           string  `json:"id"`
			Name         string  `json:"name"`
			Memory       float64 `json:"memory"`
			Secure       bool    `json:"secure"`
			Availability string  `json:"availability"`
			Price        struct {
				Secure float64 `json:"secure"`
			} `json:"price"`
		} `json:"gpus"`
	}
	// Availability changes by the minute; it is a hint, not a promise.
	if err := p.request(http.MethodGet, "/catalog/gpus?include=AVAILABILITY&product=POD&cloud=SECURE", nil, &gpus); err != nil {
		return nil, err
	}
	var cpus struct {
		CPUs []struct {
			ID           string  `json:"id"`
			Name         string  `json:"name"`
			RAMGbPerVcpu float64 `json:"ramGbPerVcpu"`
			Price        struct {
				SecurePerVcpu float64 `json:"securePerVcpu"`
			} `json:"price"`
		} `json:"cpus"`
	}
	if err := p.request(http.MethodGet, "/catalog/cpus", nil, &cpus); err != nil {
		return nil, err
	}

	var options []runpodOption
	for _, cpu := range cpus.CPUs {
		hourly := cpu.Price.SecurePerVcpu * runpodCPUVcpus
		if hourly <= 0 {
			continue
		}
		options = append(options, runpodOption{
			hardware: computeHardware{
				name:   runpodSlug(cpu.ID),
				kind:   "CPU",
				memory: fmt.Sprintf("%d vCPU, %g GB RAM", runpodCPUVcpus, cpu.RAMGbPerVcpu*runpodCPUVcpus),
				rate:   fmt.Sprintf("$%.2f/h", hourly),
				paid:   hourly > hfConfirmAboveUSD,
			},
			hourly: hourly,
			cpuID:  cpu.ID,
		})
	}
	sort.Slice(options, func(i, j int) bool { return options[i].hourly < options[j].hourly })

	var gpuOptions []runpodOption
	seen := map[string]bool{}
	for _, gpu := range gpus.GPUs {
		if !gpu.Secure || gpu.Price.Secure <= 0 {
			continue
		}
		label := gpu.Name
		if label == "" {
			label = gpu.ID
		}
		slug := runpodSlug(label)
		if seen[slug] {
			slug = fmt.Sprintf("%s-%dgb", slug, int(gpu.Memory))
		}
		seen[slug] = true
		option := runpodOption{
			hardware: computeHardware{
				name:   slug,
				kind:   "GPU",
				memory: fmt.Sprintf("%g GB VRAM", gpu.Memory),
				rate:   fmt.Sprintf("$%.2f/h", gpu.Price.Secure),
				paid:   true,
			},
			hourly:    gpu.Price.Secure,
			gpuID:     gpu.ID,
			memoryGB:  gpu.Memory,
			available: gpu.Availability != "NONE",
		}
		switch gpu.Availability {
		case "NONE":
			option.hardware.note = "none free"
		case "LOW":
			option.hardware.note = "few free"
		}
		gpuOptions = append(gpuOptions, option)
	}
	// Free GPUs first, each group by price.
	sort.SliceStable(gpuOptions, func(i, j int) bool {
		if gpuOptions[i].available != gpuOptions[j].available {
			return gpuOptions[i].available
		}
		return gpuOptions[i].hourly < gpuOptions[j].hourly
	})
	options = append(options, gpuOptions...)

	p.mu.Lock()
	p.options = options
	p.mu.Unlock()
	return options, nil
}

// forgetHardware drops the cached list, so a long-running hi server sees
// current prices and availability.
func (p *runpodProvider) forgetHardware() {
	p.mu.Lock()
	p.options = nil
	p.mu.Unlock()
}

func (p *runpodProvider) hardware() ([]computeHardware, error) {
	options, err := p.hardwareOptions()
	if err != nil {
		return nil, err
	}
	hardware := make([]computeHardware, len(options))
	for i, option := range options {
		hardware[i] = option.hardware
	}
	return hardware, nil
}

// freeAlternatives names up to three cheapest GPUs that RunPod reports as
// free, with at least the memory of the one that was not.
func (p *runpodProvider) freeAlternatives(name string) string {
	p.mu.Lock()
	p.options = nil // availability is stale by now
	p.mu.Unlock()
	wanted, err := p.option(name)
	if err != nil || wanted.gpuID == "" {
		return ""
	}
	options, _ := p.hardwareOptions()
	var names []string
	for _, option := range options {
		if option.gpuID != "" && option.available && option.memoryGB >= wanted.memoryGB && option.hardware.name != name {
			names = append(names, fmt.Sprintf("%s (%s)", option.hardware.name, option.hardware.rate))
			if len(names) == 3 {
				break
			}
		}
	}
	return strings.Join(names, ", ")
}

func (p *runpodProvider) option(name string) (runpodOption, error) {
	options, err := p.hardwareOptions()
	if err != nil {
		return runpodOption{}, err
	}
	for _, option := range options {
		if option.hardware.name == name {
			return option, nil
		}
	}
	return runpodOption{}, fmt.Errorf("unknown runpod hardware %q", name)
}

func (*runpodProvider) validateRun(runRequest) error {
	return usageError{"runpod has no run-to-completion jobs yet; start a pod with `hi compute up --on runpod` and run your script over `hi compute ssh`"}
}

func (*runpodProvider) runCommand(runRequest) []string { return nil }

func (*runpodProvider) runJob(runRequest, io.Reader, io.Writer, io.Writer) (int, error) {
	return 0, errors.New("runpod runs are not supported")
}

func (*runpodProvider) wait(string, io.Writer, io.Writer) (int, error) {
	return 0, errors.New("runpod has no runs to wait for")
}

func (p *runpodProvider) podSpec(request upRequest) (map[string]any, error) {
	option, err := p.option(request.hardware.name)
	if err != nil {
		return nil, err
	}
	image := request.image
	if image == "" {
		image = runpodImage
	}
	env := map[string]string{"HI_MANAGED": "1"}
	key := request.publicKey
	if key == "" {
		key = runpodPublicKey()
	}
	if key != "" {
		env["PUBLIC_KEY"] = key
	}
	spec := map[string]any{
		"name":     request.name,
		"image":    image,
		"cloud":    "SECURE",
		"disk":     50,
		"ports":    []string{"22/tcp"},
		"startSsh": true,
		"env":      env,
	}
	if option.gpuID != "" {
		spec["gpu"] = map[string]any{"id": option.gpuID, "count": 1}
	} else {
		spec["cpu"] = map[string]any{"id": option.cpuID, "vcpuCount": runpodCPUVcpus}
		spec["disk"] = 20
	}
	return spec, nil
}

func (p *runpodProvider) upCommand(request upRequest) []string {
	spec, err := p.podSpec(request)
	if err != nil {
		return []string{"(could not build the request: " + err.Error() + ")"}
	}
	if env, ok := spec["env"].(map[string]string); ok && env["PUBLIC_KEY"] != "" {
		env["PUBLIC_KEY"] = "<your ~/.ssh public key>"
	}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(spec)
	return []string{"POST", runpodAPIBase + "/pods", strings.TrimSpace(data.String())}
}

// create starts a pod, waits until it can be reached over SSH, and installs
// the lifetime watchdog on it.
func (p *runpodProvider) create(request upRequest, stdout, stderr io.Writer) error {
	if request.publicKey == "" && runpodPublicKey() == "" {
		return errors.New("no SSH public key found; run `ssh-keygen -t ed25519` first")
	}
	spec, err := p.podSpec(request)
	if err != nil {
		return err
	}
	var pod runpodPod
	if err := p.request(http.MethodPost, "/pods", spec, &pod); err != nil {
		if strings.Contains(err.Error(), "none of that hardware is free") {
			if alternatives := p.freeAlternatives(request.hardware.name); alternatives != "" {
				err = fmt.Errorf("%w\nFree right now with at least as much memory: %s", err, alternatives)
			}
		}
		return err
	}
	fmt.Fprintf(stdout, "Created pod %s; waiting for it to start (this can take a few minutes)...\n", pod.ID)

	pod, err = p.waitReady(pod.ID, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "hi: %v\n", err)
		fmt.Fprintf(stderr, "hi: the pod still bills; stop it with `hi compute stop %s`\n", request.name)
		return err
	}
	// A hi server has no SSH access to pods it starts for a device; its own
	// reconciler stops them at --max instead.
	if request.max != noLimit && !request.brokered {
		if err := p.installWatchdog(request.name, request.max); err != nil {
			fmt.Fprintf(stderr, "hi: warning: could not install the watchdog on the pod (%v); "+
				"the local watcher still stops it at --max while this machine is on\n", err)
		}
	}
	return nil
}

func (p *runpodProvider) getPod(id string) (runpodPod, error) {
	var pod runpodPod
	err := p.request(http.MethodGet, "/pods/"+url.PathEscape(id), nil, &pod)
	return pod, err
}

func (p *runpodProvider) waitReady(id string, stdout io.Writer) (runpodPod, error) {
	deadline := time.Now().Add(runpodReadyTimeout)
	last := ""
	for {
		pod, err := p.getPod(id)
		if err == nil {
			if pod.Status != last {
				fmt.Fprintf(stdout, "  %s\n", strings.ToLower(pod.Status))
				last = pod.Status
			}
			switch {
			case pod.Status == "RUNNING" && pod.SSH.Direct != nil && pod.SSH.Direct.Port > 0:
				return pod, nil
			case pod.Status == "ERROR" || pod.Status == "TERMINATED":
				return pod, fmt.Errorf("the pod ended while starting (%s)", strings.ToLower(pod.Status))
			}
		}
		if time.Now().After(deadline) {
			return pod, fmt.Errorf("the pod was not reachable after %s", formatDuration(runpodReadyTimeout))
		}
		time.Sleep(runpodPollEvery)
	}
}

// installWatchdog starts a detached process on the pod that terminates it
// after the lifetime, through the pod-scoped RunPod key in the pod's
// environment. The local watcher remains the guarantee if this cannot work.
func (p *runpodProvider) installWatchdog(name string, lifetime time.Duration) error {
	target, err := p.ssh(name)
	if err != nil {
		return err
	}
	script := fmt.Sprintf(`export $(tr '\0' '\n' < /proc/1/environ | grep '^RUNPOD_' | xargs)
sleep %d
runpodctl remove pod "$RUNPOD_POD_ID" || runpodctl pod delete "$RUNPOD_POD_ID" ||
  curl -fsS -X DELETE -H "Authorization: Bearer $RUNPOD_API_KEY" "https://api.runpod.io/v2/pods/$RUNPOD_POD_ID"`,
		int(lifetime.Seconds()))
	command := "mkdir -p ~/.hi && cat > ~/.hi/watchdog.sh && (setsid nohup bash ~/.hi/watchdog.sh > ~/.hi/watchdog.log 2>&1 < /dev/null &)"
	_, err = remoteOutput(target, strings.NewReader(script), time.Minute, command)
	return err
}

func (p *runpodProvider) pods() ([]runpodPod, error) {
	var all []runpodPod
	cursor := ""
	for page := 0; page < 20; page++ {
		path := "/pods?limit=100"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var result struct {
			Pods       []runpodPod `json:"pods"`
			Pagination struct {
				NextCursor  string `json:"nextCursor"`
				HasNextPage bool   `json:"hasNextPage"`
			} `json:"pagination"`
		}
		if err := p.request(http.MethodGet, path, nil, &result); err != nil {
			return nil, err
		}
		all = append(all, result.Pods...)
		if !result.Pagination.HasNextPage || result.Pagination.NextCursor == "" {
			break
		}
		cursor = result.Pagination.NextCursor
	}
	return all, nil
}

func (p *runpodProvider) hardwareName(pod runpodPod) string {
	options, _ := p.hardwareOptions()
	for _, option := range options {
		if (pod.GPU != nil && option.gpuID == pod.GPU.ID) || (pod.CPU != nil && option.cpuID == pod.CPU.ID) {
			return option.hardware.name
		}
	}
	switch {
	case pod.GPU != nil:
		return runpodSlug(pod.GPU.ID)
	case pod.CPU != nil:
		return runpodSlug(pod.CPU.ID)
	}
	return "?"
}

func (p *runpodProvider) list() ([]computeInstance, error) {
	pods, err := p.pods()
	if err != nil {
		return nil, err
	}
	var instances []computeInstance
	for _, pod := range pods {
		if pod.Status == "TERMINATED" {
			continue
		}
		name := pod.Name
		if !validLookupName(name) {
			name = strings.ToLower(pod.ID)
		}
		instance := computeInstance{
			name:     name,
			hardware: p.hardwareName(pod),
			detail:   "https://console.runpod.io/pods?id=" + pod.ID,
			state:    strings.ToLower(pod.Status),
			managed:  pod.Env["HI_MANAGED"] == "1",
		}
		if created, err := time.Parse(time.RFC3339, pod.CreatedAt); err == nil {
			instance.created = created
		}
		instances = append(instances, instance)
	}
	return instances, nil
}

func (p *runpodProvider) findPod(name string) (runpodPod, error) {
	pods, err := p.pods()
	if err != nil {
		return runpodPod{}, err
	}
	for _, pod := range pods {
		if pod.Status != "TERMINATED" && (pod.Name == name || strings.EqualFold(pod.ID, name)) {
			return pod, nil
		}
	}
	return runpodPod{}, fmt.Errorf("no RunPod pod named %q", name)
}

// stop terminates the pod: a stopped pod still bills for its disk.
func (p *runpodProvider) stop(name string, stdout, stderr io.Writer) error {
	pod, err := p.findPod(name)
	if err != nil {
		return err
	}
	if err := p.request(http.MethodDelete, "/pods/"+url.PathEscape(pod.ID), nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Stopped %s.\n", name)
	return nil
}

func (p *runpodProvider) ssh(name string) (sshTarget, error) {
	pod, err := p.findPod(name)
	if err != nil {
		return sshTarget{}, err
	}
	if pod.Status != "RUNNING" || pod.SSH.Direct == nil || pod.SSH.Direct.Port == 0 {
		if pod, err = p.waitReady(pod.ID, io.Discard); err != nil {
			return sshTarget{}, err
		}
	}
	user := pod.SSH.Direct.Username
	if user == "" {
		user = "root"
	}
	return sshTarget{
		options: []string{
			"-p", strconv.Itoa(pod.SSH.Direct.Port),
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
			"-o", "ServerAliveInterval=30",
		},
		destination: user + "@" + pod.SSH.Direct.Host,
		hint:        "hi: RunPod pods accept the key in ~/.ssh/id_ed25519.pub that hi passed when starting it",
	}, nil
}

func (p *runpodProvider) logs(name string, follow bool, lines int, stdin io.Reader, stdout, stderr io.Writer) error {
	return sshLogs(p, name, follow, lines, stdin, stdout, stderr)
}
