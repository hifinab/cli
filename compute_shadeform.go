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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	shadeformKeysURL      = "https://platform.shadeform.ai/settings/api"
	shadeformReadyTimeout = 25 * time.Minute
	shadeformManagedTag   = "hi-managed"
)

// These are replaced in tests.
var (
	shadeformAPIBase   = "https://api.shadeform.ai/v1"
	shadeformPollEvery = 10 * time.Second
)

// shadeformProvider drives Shadeform, which rents virtual machines from many
// clouds (Hyperstack, Massed Compute, Lambda, Scaleway, and more) through
// one API. A GPU type such as H100 is offered by several clouds at
// different prices, so hi names each type once, at its cheapest free offer,
// and "h100@lambdalabs" picks a cloud. Shadeform deletes an instance itself
// at --max (auto_delete), so no watcher is needed.
type shadeformProvider struct {
	mu     sync.Mutex
	offers []shadeformOffer
}

// shadeformOffer is one GPU type on one cloud, in its cheapest free region.
type shadeformOffer struct {
	hardware computeHardware
	// typeName is the short name of the GPU type; hardware.name adds
	// "@cloud" for offers that aren't the type's cheapest.
	typeName     string
	cloud        string
	region       string
	regionName   string
	instanceType string
	os           string
	hourly       float64
	vramGB       int
	available    bool
}

type shadeformInstance struct {
	ID                string   `json:"id"`
	Cloud             string   `json:"cloud"`
	Region            string   `json:"region"`
	ShadeInstanceType string   `json:"shade_instance_type"`
	Name              string   `json:"name"`
	IP                string   `json:"ip"`
	SSHUser           string   `json:"ssh_user"`
	SSHPort           int      `json:"ssh_port"`
	Status            string   `json:"status"`
	StatusDetails     string   `json:"status_details"`
	HourlyPrice       float64  `json:"hourly_price"`
	CreatedAt         string   `json:"created_at"`
	Tags              []string `json:"tags"`
	AutoDelete        *struct {
		DateThreshold string `json:"date_threshold"`
	} `json:"auto_delete"`
}

func newShadeformProvider() *shadeformProvider { return &shadeformProvider{} }

func (*shadeformProvider) name() string { return "shadeform" }

func (*shadeformProvider) maxLifetime() time.Duration { return 0 }

// Shadeform deletes instances itself at their auto_delete time.
func (*shadeformProvider) enforcesLifetime() bool { return true }

func (*shadeformProvider) reservedPorts() map[int]string { return nil }

func (*shadeformProvider) account() (string, error) { return "", nil }

func shadeformKeyPath() string { return filepath.Join(hiConfigDirectory(), "shadeform_key") }

// shadeformToken reads SHADEFORM_API_KEY, then the key `hi login shadeform`
// saved. It is held in memory only.
func shadeformToken() string {
	if token := strings.TrimSpace(os.Getenv("SHADEFORM_API_KEY")); token != "" {
		return token
	}
	data, err := os.ReadFile(shadeformKeyPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (*shadeformProvider) check() providerStatus {
	if shadeformToken() == "" {
		return providerStatus{installed: true, hints: []string{"sign in with `hi login shadeform`"}}
	}
	status := providerStatus{installed: true, signedIn: true}
	if runpodPublicKey() == "" {
		status.hints = append(status.hints, "ssh and tunnels need a key: run `ssh-keygen -t ed25519`")
	}
	return status
}

// signIn checks a key against Shadeform and saves it for hi.
func (p *shadeformProvider) signIn(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, "\"\n\r ") {
		return "", errors.New("that does not look like a Shadeform API key")
	}
	if err := shadeformRequest(key, http.MethodGet, "/sshkeys", nil, nil); err != nil {
		return "", err
	}
	path := shadeformKeyPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return "", err
	}
	p.forgetHardware()
	return path, nil
}

func (p *shadeformProvider) request(method, path string, body, result any) error {
	token := shadeformToken()
	if token == "" {
		return errors.New("not signed in to Shadeform; run `hi login shadeform`")
	}
	return shadeformRequest(token, method, path, body, result)
}

func shadeformRequest(token, method, path string, body, result any) error {
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
	request, err := http.NewRequestWithContext(ctx, method, shadeformAPIBase+path, payload)
	if err != nil {
		return err
	}
	request.Header.Set("X-API-KEY", token)
	request.Header.Set("User-Agent", "hi/"+version)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("Shadeform API: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode >= 300 {
		return shadeformError(response.StatusCode, data)
	}
	if result == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, result)
}

// shadeformError explains Shadeform's errors without the key.
func shadeformError(status int, data []byte) error {
	var problem struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(data, &problem)
	detail := strings.TrimSpace(problem.Message + " " + problem.Error)
	if detail == "" {
		detail = strings.TrimSpace(string(data))
		if len(detail) > 200 {
			detail = detail[:200] + "..."
		}
	}
	lower := strings.ToLower(detail)
	switch {
	case status == http.StatusUnauthorized:
		return errors.New("Shadeform rejected the API key; run `hi login shadeform` again")
	case status == http.StatusPaymentRequired || strings.Contains(lower, "balance") || strings.Contains(lower, "payment"):
		return fmt.Errorf("Shadeform needs a payment method or more balance: %s\nAdd one at https://platform.shadeform.ai/settings/billing", detail)
	case strings.Contains(lower, "availab") || strings.Contains(lower, "capacity") || strings.Contains(lower, "out of stock"):
		return noCapacityError{fmt.Errorf("Shadeform could not start it: %s\nThat hardware is probably taken right now; try another --gpu", detail)}
	case status == http.StatusTooManyRequests:
		return errors.New("Shadeform is rate limiting requests; try again in a minute")
	}
	return fmt.Errorf("Shadeform API returned %d: %s", status, detail)
}

// shadeformSlug makes "a100-sxm4-80g" from "A100_sxm4_80G".
func shadeformSlug(name string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

func (p *shadeformProvider) forgetHardware() {
	p.mu.Lock()
	p.offers = nil
	p.mu.Unlock()
}

// allOffers lists every single-GPU virtual machine offer, cheapest first.
func (p *shadeformProvider) allOffers() ([]shadeformOffer, error) {
	p.mu.Lock()
	cached := p.offers
	p.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	var result struct {
		InstanceTypes []struct {
			Cloud             string  `json:"cloud"`
			ShadeInstanceType string  `json:"shade_instance_type"`
			HourlyPrice       float64 `json:"hourly_price"`
			DeploymentType    string  `json:"deployment_type"`
			Configuration     struct {
				NumGPUs         int      `json:"num_gpus"`
				GPUType         string   `json:"gpu_type"`
				VRAMPerGPUInGB  int      `json:"vram_per_gpu_in_gb"`
				MemoryInGB      int      `json:"memory_in_gb"`
				VCPUs           int      `json:"vcpus"`
				OSOptions       []string `json:"os_options"`
				GPUManufacturer string   `json:"gpu_manufacturer"`
			} `json:"configuration"`
			Availability []struct {
				Region      string `json:"region"`
				Available   bool   `json:"available"`
				DisplayName string `json:"display_name"`
				RentalType  string `json:"rental_type"`
			} `json:"availability"`
		} `json:"instance_types"`
	}
	if err := p.request(http.MethodGet, "/instances/types?num_gpus=1&sort=price", nil, &result); err != nil {
		return nil, err
	}
	var offers []shadeformOffer
	for _, item := range result.InstanceTypes {
		config := item.Configuration
		if config.NumGPUs != 1 || item.HourlyPrice <= 0 || (item.DeploymentType != "" && item.DeploymentType != "vm") {
			continue
		}
		offer := shadeformOffer{
			typeName:     shadeformSlug(item.ShadeInstanceType),
			cloud:        item.Cloud,
			instanceType: item.ShadeInstanceType,
			hourly:       item.HourlyPrice / 100,
			vramGB:       config.VRAMPerGPUInGB,
			os:           shadeformOS(config.OSOptions),
		}
		// Only on-demand: a spot machine can be taken back mid-run.
		for _, region := range item.Availability {
			if region.RentalType != "" && region.RentalType != "on_demand" {
				continue
			}
			if offer.region == "" || (region.Available && !offer.available) {
				offer.region, offer.regionName, offer.available = region.Region, region.DisplayName, region.Available
			}
		}
		if offer.region == "" {
			continue
		}
		offer.hardware = computeHardware{
			name:   offer.typeName + "@" + offer.cloud,
			kind:   "GPU",
			memory: fmt.Sprintf("%d GB VRAM", config.VRAMPerGPUInGB),
			rate:   fmt.Sprintf("$%.2f/h", offer.hourly),
			paid:   true,
			note:   offer.cloud + ", " + offer.regionName,
		}
		if !offer.available {
			offer.hardware.note = offer.cloud + ", none free"
		}
		offers = append(offers, offer)
	}
	sort.SliceStable(offers, func(i, j int) bool {
		if offers[i].available != offers[j].available {
			return offers[i].available
		}
		return offers[i].hourly < offers[j].hourly
	})
	p.mu.Lock()
	p.offers = offers
	p.mu.Unlock()
	return offers, nil
}

// shadeformOS prefers an image with CUDA.
func shadeformOS(options []string) string {
	for _, option := range options {
		if strings.Contains(option, "cuda") {
			return option
		}
	}
	if len(options) > 0 {
		return options[0]
	}
	return ""
}

// hardware lists each GPU type once, at its cheapest free offer.
func (p *shadeformProvider) hardware() ([]computeHardware, error) {
	offers, err := p.allOffers()
	if err != nil {
		return nil, err
	}
	var hardware []computeHardware
	seen := map[string]bool{}
	for _, offer := range offers {
		if seen[offer.typeName] {
			continue
		}
		seen[offer.typeName] = true
		option := offer.hardware
		option.name = offer.typeName
		hardware = append(hardware, option)
	}
	return hardware, nil
}

// offer finds "h100" (the cheapest free H100) or "h100@lambdalabs".
func (p *shadeformProvider) offer(name string) (shadeformOffer, error) {
	offers, err := p.allOffers()
	if err != nil {
		return shadeformOffer{}, err
	}
	name = strings.ToLower(name)
	for _, offer := range offers {
		if offer.hardware.name == name || offer.typeName == name {
			return offer, nil
		}
	}
	return shadeformOffer{}, fmt.Errorf("unknown shadeform hardware %q", name)
}

// lookupHardware accepts names outside the hardware list, such as
// "h100@lambdalabs".
func (p *shadeformProvider) lookupHardware(name string) (computeHardware, error) {
	offer, err := p.offer(name)
	if err != nil {
		return computeHardware{}, err
	}
	hardware := offer.hardware
	if !strings.Contains(name, "@") {
		hardware.name = offer.typeName
	}
	return hardware, nil
}

// alternatives lists free offers of the same or other GPU types with at
// least as much memory, at most ceiling dollars an hour, cheapest first.
func (p *shadeformProvider) alternatives(name string, ceiling float64) ([]computeHardware, error) {
	wanted, err := p.offer(name)
	if err != nil {
		return nil, err
	}
	p.forgetHardware()
	offers, err := p.allOffers()
	if err != nil {
		return nil, err
	}
	var result []computeHardware
	for _, offer := range offers {
		if !offer.available || offer.hardware.name == wanted.hardware.name ||
			offer.vramGB < wanted.vramGB || offer.hourly > ceiling+1e-9 {
			continue
		}
		result = append(result, offer.hardware)
	}
	return result, nil
}

func (*shadeformProvider) validateRun(runRequest) error {
	return usageError{"shadeform has no run-to-completion jobs; start a machine with `hi compute up --on shadeform` and run your script over `hi compute ssh`"}
}

func (*shadeformProvider) runCommand(runRequest) []string { return nil }

func (*shadeformProvider) runJob(runRequest, io.Reader, io.Writer, io.Writer) (int, error) {
	return 0, errors.New("shadeform runs are not supported")
}

func (*shadeformProvider) wait(string, io.Writer, io.Writer) (int, error) {
	return 0, errors.New("shadeform has no runs to wait for")
}

// createBody is the create request, without the SSH key ID, which is only
// known after the key is registered.
func (p *shadeformProvider) createBody(request upRequest) (map[string]any, error) {
	if request.image != "" {
		return nil, usageError{"--image does not apply to Shadeform, which starts virtual machines"}
	}
	offer, err := p.offer(request.hardware.name)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"cloud":               offer.cloud,
		"region":              offer.region,
		"shade_instance_type": offer.instanceType,
		"shade_cloud":         true,
		"name":                request.name,
		"tags":                []string{shadeformManagedTag},
	}
	if offer.os != "" {
		body["os"] = offer.os
	}
	if request.max != noLimit {
		body["auto_delete"] = map[string]string{
			"date_threshold": computeNow().Add(request.max).UTC().Format(time.RFC3339),
		}
	}
	return body, nil
}

func (p *shadeformProvider) upCommand(request upRequest) []string {
	body, err := p.createBody(request)
	if err != nil {
		return []string{"(could not build the request: " + err.Error() + ")"}
	}
	body["ssh_key_id"] = "<your ~/.ssh public key>"
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(body)
	return []string{"POST", shadeformAPIBase + "/instances/create", strings.TrimSpace(data.String())}
}

// sshKeyID registers the public key with Shadeform once and returns its ID.
func (p *shadeformProvider) sshKeyID(publicKey string) (string, error) {
	var keys struct {
		SSHKeys []struct {
			ID        string `json:"id"`
			PublicKey string `json:"public_key"`
		} `json:"ssh_keys"`
	}
	if err := p.request(http.MethodGet, "/sshkeys", nil, &keys); err != nil {
		return "", err
	}
	fields := strings.Fields(publicKey)
	if len(fields) < 2 {
		return "", errors.New("the SSH public key is malformed")
	}
	for _, key := range keys.SSHKeys {
		if existing := strings.Fields(key.PublicKey); len(existing) >= 2 && existing[1] == fields[1] {
			return key.ID, nil
		}
	}
	name := "hi"
	if len(fields) >= 3 {
		name = "hi " + fields[2]
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := p.request(http.MethodPost, "/sshkeys/add", map[string]string{"name": name, "public_key": publicKey}, &added); err != nil {
		return "", fmt.Errorf("register the SSH key with Shadeform: %w", err)
	}
	return added.ID, nil
}

// create starts a machine and waits until it can be reached over SSH.
func (p *shadeformProvider) create(request upRequest, stdout, stderr io.Writer) error {
	publicKey := request.publicKey
	if publicKey == "" {
		publicKey = runpodPublicKey()
	}
	if publicKey == "" {
		return errors.New("no SSH public key found; run `ssh-keygen -t ed25519` first")
	}
	body, err := p.createBody(request)
	if err != nil {
		return err
	}
	keyID, err := p.sshKeyID(publicKey)
	if err != nil {
		return err
	}
	body["ssh_key_id"] = keyID
	var created struct {
		ID string `json:"id"`
	}
	if err := p.request(http.MethodPost, "/instances/create", body, &created); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Created %s on %s (%s); waiting for it to boot (usually 3-8 minutes)...\n",
		created.ID, body["cloud"], body["region"])
	if _, err := p.waitReady(created.ID, stdout); err != nil {
		fmt.Fprintf(stderr, "hi: %v\n", err)
		fmt.Fprintf(stderr, "hi: it may still bill; stop it with `hi compute stop %s`\n", request.name)
		return err
	}
	return nil
}

func (p *shadeformProvider) info(id string) (shadeformInstance, error) {
	var instance shadeformInstance
	err := p.request(http.MethodGet, "/instances/"+url.PathEscape(id)+"/info", nil, &instance)
	return instance, err
}

func (p *shadeformProvider) waitReady(id string, stdout io.Writer) (shadeformInstance, error) {
	deadline := time.Now().Add(shadeformReadyTimeout)
	last := ""
	for {
		instance, err := p.info(id)
		if err == nil {
			state := instance.Status
			if instance.StatusDetails != "" {
				state += " (" + instance.StatusDetails + ")"
			}
			if state != last {
				fmt.Fprintf(stdout, "  %s\n", state)
				last = state
			}
			switch {
			case instance.Status == "active" && instance.IP != "":
				return instance, nil
			case instance.Status == "error" || instance.Status == "deleting" || instance.Status == "deleted":
				return instance, fmt.Errorf("the machine ended while starting (%s)", state)
			}
		}
		if time.Now().After(deadline) {
			return instance, fmt.Errorf("the machine was not reachable after %s", formatDuration(shadeformReadyTimeout))
		}
		time.Sleep(shadeformPollEvery)
	}
}

func (p *shadeformProvider) instances() ([]shadeformInstance, error) {
	var result struct {
		Instances []shadeformInstance `json:"instances"`
	}
	if err := p.request(http.MethodGet, "/instances", nil, &result); err != nil {
		return nil, err
	}
	return result.Instances, nil
}

func (p *shadeformProvider) list() ([]computeInstance, error) {
	machines, err := p.instances()
	if err != nil {
		return nil, err
	}
	var result []computeInstance
	for _, machine := range machines {
		if machine.Status == "deleting" || machine.Status == "deleted" {
			continue
		}
		name := machine.Name
		if !validLookupName(name) {
			name = strings.ToLower(machine.ID)
		}
		instance := computeInstance{
			name:     name,
			hardware: shadeformSlug(machine.ShadeInstanceType) + "@" + machine.Cloud,
			detail:   fmt.Sprintf("https://platform.shadeform.ai/instances/%s (%s)", machine.ID, machine.Region),
			state:    machine.Status,
		}
		for _, tag := range machine.Tags {
			instance.managed = instance.managed || tag == shadeformManagedTag
		}
		if created, err := time.Parse(time.RFC3339, machine.CreatedAt); err == nil {
			instance.created = created
		}
		if machine.AutoDelete != nil {
			if deadline, err := time.Parse(time.RFC3339, machine.AutoDelete.DateThreshold); err == nil {
				instance.deadline = deadline
			}
		}
		result = append(result, instance)
	}
	return result, nil
}

func (p *shadeformProvider) find(name string) (shadeformInstance, error) {
	machines, err := p.instances()
	if err != nil {
		return shadeformInstance{}, err
	}
	for _, machine := range machines {
		if machine.Status != "deleting" && machine.Status != "deleted" &&
			(machine.Name == name || strings.EqualFold(machine.ID, name)) {
			return machine, nil
		}
	}
	return shadeformInstance{}, fmt.Errorf("no Shadeform machine named %q", name)
}

// stop deletes the machine; Shadeform stops billing once it is deleting.
func (p *shadeformProvider) stop(name string, stdout, _ io.Writer) error {
	machine, err := p.find(name)
	if err != nil {
		return err
	}
	if err := p.request(http.MethodPost, "/instances/"+url.PathEscape(machine.ID)+"/delete", nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Stopped %s.\n", name)
	return nil
}

func (p *shadeformProvider) ssh(name string) (sshTarget, error) {
	machine, err := p.find(name)
	if err != nil {
		return sshTarget{}, err
	}
	if machine.Status != "active" || machine.IP == "" {
		if machine, err = p.waitReady(machine.ID, io.Discard); err != nil {
			return sshTarget{}, err
		}
	}
	user := machine.SSHUser
	if user == "" {
		user = "shadeform"
	}
	port := machine.SSHPort
	if port == 0 {
		port = 22
	}
	return sshTarget{
		options: []string{
			"-p", strconv.Itoa(port),
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
			"-o", "ServerAliveInterval=30",
		},
		destination: user + "@" + machine.IP,
		hint:        "hi: Shadeform machines accept the key in ~/.ssh/id_ed25519.pub that hi registered when starting it",
	}, nil
}

func (p *shadeformProvider) logs(name string, follow bool, lines int, stdin io.Reader, stdout, stderr io.Writer) error {
	return sshLogs(p, name, follow, lines, stdin, stdout, stderr)
}
