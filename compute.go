package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
)

const (
	defaultInstanceMax = 4 * time.Hour
	defaultRunMax      = time.Hour
	watchInterval      = 30 * time.Second
)

// computeProvider is the provider-specific part of `hi compute`. Everything
// else (limits, state, confirmation, SSH, tunnels) is shared.
type computeProvider interface {
	name() string
	check() providerStatus
	hardware() ([]computeHardware, error)
	// maxLifetime is the provider's own limit, or zero for none.
	maxLifetime() time.Duration
	// enforcesLifetime reports whether the provider stops instances at --max
	// itself; otherwise hi runs a local watcher.
	enforcesLifetime() bool
	reservedPorts() map[int]string
	account() (string, error)
	validateRun(request runRequest) error
	create(request upRequest, stdout, stderr io.Writer) error
	list() ([]computeInstance, error)
	stop(name string, stdout, stderr io.Writer) error
	ssh(name string) (sshTarget, error)
	runJob(request runRequest, stdin io.Reader, stdout, stderr io.Writer) (int, error)
	logs(name string, follow bool, lines int, stdin io.Reader, stdout, stderr io.Writer) error
	wait(name string, stdout, stderr io.Writer) (int, error)
	upCommand(request upRequest) []string
	runCommand(request runRequest) []string
}

type providerStatus struct {
	installed bool
	signedIn  bool
	hints     []string
}

type computeHardware struct {
	name   string
	kind   string
	memory string
	rate   string
	paid   bool
}

type computeInstance struct {
	name     string
	hardware string
	shape    string
	detail   string
	state    string
}

type upRequest struct {
	name      string
	hardware  computeHardware
	highMem   bool
	max       time.Duration
	image     string
	namespace string
	// serve, when set, makes the instance run this model server itself.
	serve *serveRecipe
}

// runRequest is either a Python script (script, args) or a container image
// with a command (image, command).
type runRequest struct {
	name      string
	hardware  computeHardware
	highMem   bool
	max       time.Duration
	env       []string
	secrets   []string
	detach    bool
	namespace string
	script    string
	args      []string
	image     string
	command   []string
}

type sshTarget struct {
	options     []string
	destination string
}

// computeRecord is what hi remembers locally about instances it started.
type computeRecord struct {
	Name       string    `json:"name"`
	Provider   string    `json:"provider"`
	Hardware   string    `json:"hardware"`
	Created    time.Time `json:"created"`
	Deadline   time.Time `json:"deadline"`
	WatcherPID int       `json:"watcher_pid,omitempty"`
	Namespace  string    `json:"namespace,omitempty"`
}

var (
	computeProviders = []computeProvider{colabProvider{}, newHFProvider()}
	computeNow       = time.Now
	// startComputeWatcher is replaced in tests so no background process starts.
	startComputeWatcher = spawnComputeWatcher
)

func runCompute(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		if !isTerminal(stdin) {
			printComputeUsage(stdout)
			return 0
		}
		return exitCode(computeMenu(stdin, stdout, stderr), stderr)
	}

	command, rest := args[0], args[1:]
	switch command {
	case "help", "-h", "--help":
		printComputeUsage(stdout)
		return 0
	case "providers":
		return exitCode(computeProvidersCommand(stdout), stderr)
	case "hardware":
		return exitCode(computeHardwareCommand(rest, stdout, stderr), stderr)
	case "up":
		return exitCode(computeUpCommand(rest, stdin, stdout, stderr), stderr)
	case "ls":
		return exitCode(computeListCommand(rest, stdin, stdout, stderr), stderr)
	case "status":
		return exitCode(computeStatusCommand(rest, stdout, stderr), stderr)
	case "ssh":
		return exitCode(computeSSHCommand(rest, stdin, stdout, stderr), stderr)
	case "tunnel":
		return exitCode(computeTunnelCommand(rest, stdin, stdout, stderr), stderr)
	case "stop":
		return exitCode(computeStopCommand(rest, stdin, stdout, stderr), stderr)
	case "run":
		code, err := computeRunCommand(rest, stdin, stdout, stderr)
		if err != nil {
			return exitCode(err, stderr)
		}
		return code
	case "serve":
		return exitCode(computeServeCommand(rest, stdin, stdout, stderr), stderr)
	case "logs":
		return exitCode(computeLogsCommand(rest, stdin, stdout, stderr), stderr)
	case "wait":
		code, err := computeWaitCommand(rest, stdout, stderr)
		if err != nil {
			return exitCode(err, stderr)
		}
		return code
	case "proxy":
		return exitCode(computeProxyCommand(rest, stdin, stdout, stderr), stderr)
	case "__watch":
		if len(rest) != 1 {
			return 2
		}
		return exitCode(watchComputeInstance(rest[0], stdout, stderr), stderr)
	default:
		fmt.Fprintf(stderr, "hi: unknown compute command %q\n\n", command)
		printComputeUsage(stderr)
		return 2
	}
}

type usageError struct{ message string }

func (e usageError) Error() string { return e.message }

func exitCode(err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, "hi: %v\n", err)
	var usage usageError
	if errors.As(err, &usage) {
		return 2
	}
	return 1
}

func printComputeUsage(w io.Writer) {
	fmt.Fprintln(w, `hi compute runs work on rented remote machines.

Usage:
  hi compute                          Guided menu (in a terminal)
  hi compute providers                Show configured providers
  hi compute hardware [--on P]        List hardware and prices
  hi compute up [options]             Start an instance
  hi compute ls                       List instances
  hi compute status <name>            Show one instance
  hi compute ssh <name> [-- cmd...]   Open a shell or run one command
  hi compute tunnel <name> <port>[:<local>]
                                      Forward a remote port to localhost
  hi compute logs <name> [--follow]   Show setup and server logs
  hi compute stop <name> | --all      Stop instances
  hi compute serve <recipe | owner/repo-GGUF --quant Q> [options]
                                      Serve a model with llama.cpp and tunnel
                                      its OpenAI-compatible API here
  hi compute run [options] <script.py> [-- args...]
  hi compute run [options] <image> -- <command...>
                                      Run to completion, streaming logs
  hi compute wait <name>...           Wait for detached runs to finish

Options for up and run:
  --on <provider>    colab or hf (default: inferred from --gpu, or
                     $HI_COMPUTE_PROVIDER, or the only signed-in provider)
  --gpu <hardware>   Hardware name from hi compute hardware (default: CPU)
  --name <name>      Instance name (default: generated)
  --max <duration>   Stop after this long, e.g. 30m or 4h
  --high-mem         Request a high-RAM machine (Colab)
  --env KEY=VALUE    Environment variable for run; repeatable
  --secret KEY       Pass $KEY as an encrypted secret (Hugging Face)
  --detach           run: return after starting (Hugging Face)
  --image <image>    Container image (Hugging Face)
  --namespace <ns>   Account or organization to bill (Hugging Face)
  --yes              Skip the cost confirmation
  --dry-run          Show what would happen without starting anything

Options for serve (plus --on, --gpu, --name, --max, --yes, --dry-run):
  --quant <quant>    GGUF quant, e.g. Q4_K_M (recipes have a default)
  --ctx <tokens>     Context length
  --port <port>      Local port for the API (default 8080)
  --args "<args>"    Extra llama-server arguments

Providers: colab, hf
Recipes:   qwen3.8-flash-next (Colab G4, Hugging Face rtx-pro-6000)`)
}

// ---------------------------------------------------------------------------
// providers and hardware

func computeProvidersCommand(stdout io.Writer) error {
	for _, provider := range computeProviders {
		status := provider.check()
		state := "ready"
		switch {
		case !status.installed:
			state = "not installed"
		case !status.signedIn:
			state = "not signed in"
		}
		fmt.Fprintf(stdout, "%-8s %s\n", provider.name(), state)
		for _, hint := range status.hints {
			fmt.Fprintf(stdout, "         %s\n", hint)
		}
	}
	return nil
}

func computeHardwareCommand(args []string, stdout, stderr io.Writer) error {
	flags := newComputeFlags("hardware", stderr)
	on := flags.String("on", "", "provider")
	if err := parseComputeFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{"usage: hi compute hardware [--on <provider>]"}
	}
	providers := computeProviders
	if *on != "" {
		provider, err := providerByName(*on)
		if err != nil {
			return err
		}
		providers = []computeProvider{provider}
	}
	for _, provider := range providers {
		table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(table, "PROVIDER\tHARDWARE\tKIND\tMEMORY\tRATE")
		options, err := provider.hardware()
		if err != nil {
			fmt.Fprintf(stderr, "hi: %s hardware: %v\n", provider.name(), err)
			continue
		}
		for _, hardware := range options {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n",
				provider.name(), hardware.name, hardware.kind, hardware.memory, hardware.rate)
		}
		table.Flush()
		if account, err := provider.account(); err == nil && account != "" {
			fmt.Fprintf(stdout, "\n%s\n", account)
		}
	}
	return nil
}

func providerByName(name string) (computeProvider, error) {
	for _, provider := range computeProviders {
		if provider.name() == strings.ToLower(name) {
			return provider, nil
		}
	}
	return nil, usageError{fmt.Sprintf("unknown provider %q; available: %s", name, providerNames())}
}

func providerNames() string {
	names := make([]string, 0, len(computeProviders))
	for _, provider := range computeProviders {
		names = append(names, provider.name())
	}
	return strings.Join(names, ", ")
}

// resolveProvider picks --on, then $HI_COMPUTE_PROVIDER, then the only ready
// provider that offers the requested hardware.
func resolveProvider(on, hardware string) (computeProvider, error) {
	if on == "" {
		on = os.Getenv("HI_COMPUTE_PROVIDER")
	}
	if on != "" {
		return providerByName(on)
	}
	var ready []computeProvider
	for _, provider := range computeProviders {
		if status := provider.check(); status.installed && status.signedIn {
			ready = append(ready, provider)
		}
	}
	if len(ready) > 1 && hardware != "" {
		var offering []computeProvider
		for _, provider := range ready {
			if _, err := resolveHardware(provider, hardware); err == nil {
				offering = append(offering, provider)
			}
		}
		if len(offering) == 1 {
			return offering[0], nil
		}
	}
	switch len(ready) {
	case 1:
		return ready[0], nil
	case 0:
		return nil, usageError{"no provider is ready; run `hi compute providers`"}
	default:
		return nil, usageError{fmt.Sprintf(
			"several providers are ready (%s); choose one with --on or set HI_COMPUTE_PROVIDER", providerNames())}
	}
}

// resolveHardware finds a hardware name; without one it picks the provider's
// first, cheapest CPU option.
func resolveHardware(provider computeProvider, name string) (computeHardware, error) {
	options, err := provider.hardware()
	if err != nil {
		return computeHardware{}, err
	}
	if name == "" && len(options) > 0 {
		return options[0], nil
	}
	var names []string
	for _, hardware := range options {
		if strings.EqualFold(hardware.name, name) {
			return hardware, nil
		}
		names = append(names, hardware.name)
	}
	return computeHardware{}, usageError{fmt.Sprintf(
		"unknown %s hardware %q; choose one of: %s", provider.name(), name, strings.Join(names, ", "))}
}

// ---------------------------------------------------------------------------
// up

func computeUpCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := newComputeFlags("up", stderr)
	on := flags.String("on", "", "provider")
	gpu := flags.String("gpu", "", "hardware")
	name := flags.String("name", "", "instance name")
	maxLifetime := flags.Duration("max", defaultInstanceMax, "maximum lifetime")
	highMem := flags.Bool("high-mem", false, "high-RAM machine")
	yes := flags.Bool("yes", false, "skip confirmation")
	dryRun := flags.Bool("dry-run", false, "show without starting")
	image := flags.String("image", "", "container image")
	namespace := flags.String("namespace", "", "account or organization to bill")
	if err := parseComputeFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{"usage: hi compute up [--on P] [--gpu HW] [--name N] [--max D] [--image I] [--namespace NS] [--high-mem] [--yes] [--dry-run]"}
	}

	provider, err := resolveProvider(*on, *gpu)
	if err != nil {
		return err
	}
	request := upRequest{highMem: *highMem, max: *maxLifetime, image: *image, namespace: *namespace}
	if request.hardware, err = resolveHardware(provider, *gpu); err != nil {
		return err
	}
	if err := validateLifetime(provider, request.max); err != nil {
		return err
	}
	if request.name, err = chooseInstanceName(*name, request.hardware.name); err != nil {
		return err
	}
	return startInstance(provider, request, *yes, *dryRun, stdin, stdout, stderr)
}

func startInstance(
	provider computeProvider,
	request upRequest,
	yes, dryRun bool,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	if provider.name() == "colab" && (request.image != "" || request.namespace != "") {
		return usageError{"--image and --namespace do not apply to Colab"}
	}
	records, err := loadComputeRecords()
	if err != nil {
		return err
	}
	if _, exists := records[request.name]; exists {
		return fmt.Errorf("an instance named %q already exists; choose another --name", request.name)
	}

	deadline := computeNow().Add(request.max)
	fmt.Fprintf(stdout, "Start %s/%s on %s (%s), stopping after %s at %s.\n",
		provider.name(), request.name, request.hardware.name, request.hardware.rate,
		formatDuration(request.max), deadline.Format("15:04"))

	if dryRun {
		fmt.Fprintf(stdout, "Would run: %s\n", strings.Join(provider.upCommand(request), " "))
		return nil
	}
	if err := requireStatus(provider); err != nil {
		return err
	}
	if request.hardware.paid {
		if err := confirm(stdin, stdout, yes, "Start it?"); err != nil {
			return err
		}
	}

	if err := provider.create(request, stdout, stderr); err != nil {
		return err
	}

	record := computeRecord{
		Name:      request.name,
		Provider:  provider.name(),
		Hardware:  request.hardware.name,
		Created:   computeNow(),
		Deadline:  deadline,
		Namespace: request.namespace,
	}
	if err := saveComputeRecord(record); err != nil {
		return err
	}
	if provider.enforcesLifetime() {
		// The provider stops the instance at its deadline itself.
	} else if pid, err := startComputeWatcher(request.name); err != nil {
		fmt.Fprintf(stderr, "hi: warning: could not start the lifetime watcher: %v\n", err)
		fmt.Fprintf(stderr, "hi: stop the instance yourself with `hi compute stop %s`\n", request.name)
	} else if pid > 0 {
		record.WatcherPID = pid
		if err := saveComputeRecord(record); err != nil {
			return err
		}
	}

	fmt.Fprintf(stdout, "\n%s is up. Next:\n", request.name)
	fmt.Fprintf(stdout, "  hi compute ssh %s\n", request.name)
	fmt.Fprintf(stdout, "  hi compute tunnel %s 8000\n", request.name)
	fmt.Fprintf(stdout, "  hi compute stop %s\n", request.name)
	return nil
}

func validateLifetime(provider computeProvider, lifetime time.Duration) error {
	if lifetime <= 0 {
		return usageError{"--max must be greater than zero"}
	}
	if limit := provider.maxLifetime(); limit > 0 && lifetime > limit {
		return usageError{fmt.Sprintf("--max %s exceeds the %s limit of %s",
			formatDuration(lifetime), provider.name(), formatDuration(limit))}
	}
	return nil
}

func chooseInstanceName(name, hardware string) (string, error) {
	if name == "" {
		suffix := make([]byte, 2)
		if _, err := rand.Read(suffix); err != nil {
			return "", err
		}
		return strings.ToLower(hardware) + "-" + hex.EncodeToString(suffix), nil
	}
	if !validInstanceName(name) {
		return "", usageError{fmt.Sprintf(
			"invalid name %q; use 1-32 lowercase letters, digits, or hyphens, starting with a letter", name)}
	}
	return name, nil
}

func validInstanceName(name string) bool {
	if len(name) == 0 || len(name) > 32 || name[0] < 'a' || name[0] > 'z' || name[len(name)-1] == '-' {
		return false
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

// validLookupName also accepts provider IDs such as Hugging Face job IDs,
// which may start with a digit.
func validLookupName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

func requireStatus(provider computeProvider) error {
	status := provider.check()
	if status.installed && status.signedIn {
		return nil
	}
	message := provider.name() + " is not ready"
	if len(status.hints) > 0 {
		message += "; " + strings.Join(status.hints, "; ")
	}
	return errors.New(message)
}

// ---------------------------------------------------------------------------
// ls, status

type listedInstance struct {
	provider string
	instance computeInstance
	record   *computeRecord
}

func computeListCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) != 0 {
		return usageError{"usage: hi compute ls"}
	}
	listed, err := listInstances(stdout, stderr)
	if err != nil {
		return err
	}
	defer func() {
		for _, provider := range computeProviders {
			if account, err := provider.account(); err == nil && account != "" {
				fmt.Fprintf(stdout, "\n%s\n", account)
			}
		}
	}()
	if len(listed) == 0 {
		fmt.Fprintln(stdout, "No instances are running.")
		return nil
	}

	now := computeNow()
	table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tPROVIDER\tHARDWARE\tUP\tSTOPS IN")
	for _, item := range listed {
		up, stopsIn := "-", "-"
		name := item.instance.name
		if item.record != nil {
			up = formatDuration(now.Sub(item.record.Created))
			stopsIn = formatDuration(item.record.Deadline.Sub(now))
		} else {
			name += " (not started by hi)"
		}
		hardware := item.instance.hardware
		if item.instance.shape != "" && item.instance.shape != "Standard" {
			hardware += " " + item.instance.shape
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", name, item.provider, hardware, up, stopsIn)
	}
	table.Flush()
	return nil
}

// listInstances reconciles provider listings with local records. It forgets
// records whose instance is gone and stops instances past their deadline.
func listInstances(stdout, stderr io.Writer) ([]listedInstance, error) {
	records, err := loadComputeRecords()
	if err != nil {
		return nil, err
	}
	now := computeNow()
	var listed []listedInstance
	for _, provider := range computeProviders {
		if status := provider.check(); !status.installed || !status.signedIn {
			continue
		}
		instances, err := provider.list()
		if err != nil {
			return nil, fmt.Errorf("list %s instances: %w", provider.name(), err)
		}
		seen := map[string]bool{}
		for _, instance := range instances {
			item := listedInstance{provider: provider.name(), instance: instance}
			if record, ok := records[instance.name]; ok && record.Provider == provider.name() {
				seen[instance.name] = true
				if !now.Before(record.Deadline) {
					fmt.Fprintf(stdout, "%s passed its maximum lifetime; stopping it.\n", instance.name)
					if err := stopInstance(provider, instance.name, stdout, stderr); err != nil {
						fmt.Fprintf(stderr, "hi: %v\n", err)
					} else {
						continue
					}
				}
				item.record = &record
			}
			listed = append(listed, item)
		}
		for name, record := range records {
			if record.Provider == provider.name() && !seen[name] {
				if err := removeComputeRecord(name); err != nil {
					return nil, err
				}
			}
		}
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].instance.name < listed[j].instance.name })
	return listed, nil
}

func computeStatusCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		return usageError{"usage: hi compute status <name>"}
	}
	name := instanceName(args[0])
	listed, err := listInstances(stdout, stderr)
	if err != nil {
		return err
	}
	for _, item := range listed {
		if item.instance.name != name {
			continue
		}
		fmt.Fprintf(stdout, "Name:      %s\n", name)
		fmt.Fprintf(stdout, "Provider:  %s\n", item.provider)
		fmt.Fprintf(stdout, "Hardware:  %s\n", item.instance.hardware)
		if item.instance.shape != "" {
			fmt.Fprintf(stdout, "Shape:     %s\n", item.instance.shape)
		}
		if item.instance.detail != "" {
			fmt.Fprintf(stdout, "Endpoint:  %s\n", item.instance.detail)
		}
		if item.record != nil {
			now := computeNow()
			fmt.Fprintf(stdout, "Started:   %s (%s ago)\n",
				item.record.Created.Format("2006-01-02 15:04"), formatDuration(now.Sub(item.record.Created)))
			fmt.Fprintf(stdout, "Stops at:  %s (in %s)\n",
				item.record.Deadline.Format("2006-01-02 15:04"), formatDuration(item.record.Deadline.Sub(now)))
		} else {
			fmt.Fprintln(stdout, "Limits:    none; not started by hi")
		}
		return nil
	}
	return fmt.Errorf("no instance named %q", name)
}

// instanceName accepts both `qwen` and `colab/qwen`.
func instanceName(argument string) string {
	if _, name, found := strings.Cut(argument, "/"); found {
		return name
	}
	return argument
}

// findInstance returns the provider running the named instance.
func findInstance(argument string) (computeProvider, string, error) {
	name := instanceName(argument)
	if !validLookupName(name) {
		return nil, "", usageError{fmt.Sprintf("invalid instance name %q", argument)}
	}
	if prefix, _, found := strings.Cut(argument, "/"); found {
		provider, err := providerByName(prefix)
		return provider, name, err
	}
	records, err := loadComputeRecords()
	if err != nil {
		return nil, "", err
	}
	if record, ok := records[name]; ok {
		provider, err := providerByName(record.Provider)
		return provider, name, err
	}
	for _, provider := range computeProviders {
		if status := provider.check(); !status.installed || !status.signedIn {
			continue
		}
		instances, err := provider.list()
		if err != nil {
			return nil, "", err
		}
		for _, instance := range instances {
			if instance.name == name {
				return provider, name, nil
			}
		}
	}
	return nil, "", fmt.Errorf("no instance named %q; see `hi compute ls`", name)
}

// ---------------------------------------------------------------------------
// ssh, tunnel, proxy

func computeSSHCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError{"usage: hi compute ssh <name> [-- command...]"}
	}
	remote := args[1:]
	if len(remote) > 0 {
		if remote[0] != "--" {
			return usageError{"usage: hi compute ssh <name> [-- command...]"}
		}
		remote = remote[1:]
	}
	provider, name, err := findInstance(args[0])
	if err != nil {
		return err
	}
	target, err := provider.ssh(name)
	if err != nil {
		return err
	}
	sshArgs := append([]string{}, target.options...)
	if len(remote) == 0 {
		sshArgs = append(sshArgs, "-t")
	}
	sshArgs = append(sshArgs, target.destination)
	sshArgs = append(sshArgs, remote...)
	return runSSH(sshArgs, stdin, stdout, stderr)
}

func computeTunnelCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) != 2 {
		return usageError{"usage: hi compute tunnel <name> <port>[:<local-port>]"}
	}
	remotePort, localPort, err := parsePortPair(args[1])
	if err != nil {
		return err
	}
	provider, name, err := findInstance(args[0])
	if err != nil {
		return err
	}
	if reason, reserved := provider.reservedPorts()[remotePort]; reserved {
		return fmt.Errorf("port %d on %s is %s; serve on another port such as 8000", remotePort, provider.name(), reason)
	}
	target, err := provider.ssh(name)
	if err != nil {
		return err
	}
	sshArgs := append([]string{"-N", "-o", "ExitOnForwardFailure=yes"}, target.options...)
	sshArgs = append(sshArgs,
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, remotePort),
		target.destination)

	fmt.Fprintf(stdout, "Forwarding http://127.0.0.1:%d to %s port %d. Press Ctrl+C to close.\n",
		localPort, name, remotePort)

	// Catch Ctrl+C here so ssh ends but hi can still offer to stop the instance.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	tunnelErr := runSSH(sshArgs, stdin, stdout, stderr)
	signal.Stop(interrupts)
	fmt.Fprintln(stdout)

	// A tunnel also ends when its instance is stopped, from another terminal
	// or by the lifetime watcher; that is not an error.
	if running, err := instanceRunning(provider, name); err == nil && !running {
		fmt.Fprintf(stdout, "Tunnel closed: %s is no longer running.\n", name)
		return nil
	}

	if isTerminal(stdin) {
		if confirm(stdin, stdout, false, fmt.Sprintf("Stop %s now?", name)) == nil {
			return stopInstance(provider, name, stdout, stderr)
		}
		fmt.Fprintf(stdout, "%s is still running. Reconnect with: hi compute tunnel %s %s\n", name, name, args[1])
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(tunnelErr, &exitErr) && len(interrupts) > 0 {
		return nil
	}
	return tunnelErr
}

func instanceRunning(provider computeProvider, name string) (bool, error) {
	instances, err := provider.list()
	if err != nil {
		return false, err
	}
	for _, instance := range instances {
		if instance.name == name {
			return true, nil
		}
	}
	return false, nil
}

func parsePortPair(value string) (int, int, error) {
	remoteText, localText, hasLocal := strings.Cut(value, ":")
	remote, err := parsePort(remoteText)
	if err != nil {
		return 0, 0, err
	}
	local := remote
	if hasLocal {
		if local, err = parsePort(localText); err != nil {
			return 0, 0, err
		}
	}
	return remote, local, nil
}

func parsePort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, usageError{fmt.Sprintf("invalid port %q", value)}
	}
	return port, nil
}

func computeProxyCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		return usageError{"usage: hi compute proxy <name>"}
	}
	provider, name, err := findInstance(args[0])
	if err != nil {
		return err
	}
	target, err := provider.ssh(name)
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(target.options); i++ {
		if value, ok := strings.CutPrefix(target.options[i+1], "ProxyCommand="); ok {
			return runInteractive(stdin, stdout, stderr, "sh", "-c", value)
		}
	}
	return fmt.Errorf("%s instances are reached directly; no proxy is needed", provider.name())
}

func runSSH(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		return errors.New("ssh is not installed; install openssh-client")
	}
	return runInteractive(stdin, stdout, stderr, ssh, args...)
}

// ---------------------------------------------------------------------------
// stop

func computeStopCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := newComputeFlags("stop", stderr)
	all := flags.Bool("all", false, "stop every instance")
	yes := flags.Bool("yes", false, "skip confirmation")
	positional, err := parseInterspersedFlags(flags, args)
	if err != nil {
		return err
	}
	if *all == (len(positional) == 1) || len(positional) > 1 {
		return usageError{"usage: hi compute stop <name> | --all [--yes]"}
	}

	if !*all {
		provider, name, err := findInstance(positional[0])
		if err != nil {
			return err
		}
		return stopInstance(provider, name, stdout, stderr)
	}

	listed, err := listInstances(stdout, stderr)
	if err != nil {
		return err
	}
	if len(listed) == 0 {
		fmt.Fprintln(stdout, "No instances are running.")
		return nil
	}
	fmt.Fprintln(stdout, "This stops:")
	for _, item := range listed {
		fmt.Fprintf(stdout, "  %s/%s (%s)\n", item.provider, item.instance.name, item.instance.hardware)
	}
	if err := confirm(stdin, stdout, *yes, "Stop all of them?"); err != nil {
		return err
	}
	var failed []string
	for _, item := range listed {
		provider, _ := providerByName(item.provider)
		if err := stopInstance(provider, item.instance.name, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "hi: %v\n", err)
			failed = append(failed, item.instance.name)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not stop: %s", strings.Join(failed, ", "))
	}
	return nil
}

func stopInstance(provider computeProvider, name string, stdout, stderr io.Writer) error {
	if err := provider.stop(name, stdout, stderr); err != nil {
		return fmt.Errorf("stop %s/%s: %w", provider.name(), name, err)
	}
	records, err := loadComputeRecords()
	if err != nil {
		return err
	}
	if record, ok := records[name]; ok && record.WatcherPID > 0 && record.WatcherPID != os.Getpid() {
		if process, err := os.FindProcess(record.WatcherPID); err == nil {
			_ = process.Signal(syscall.SIGTERM)
		}
	}
	return removeComputeRecord(name)
}

// ---------------------------------------------------------------------------
// run

func computeRunCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	flags := newComputeFlags("run", stderr)
	on := flags.String("on", "", "provider")
	gpu := flags.String("gpu", "", "hardware")
	name := flags.String("name", "", "run name")
	maxLifetime := flags.Duration("max", defaultRunMax, "maximum run time")
	highMem := flags.Bool("high-mem", false, "high-RAM machine")
	yes := flags.Bool("yes", false, "skip confirmation")
	dryRun := flags.Bool("dry-run", false, "show without starting")
	detach := flags.Bool("detach", false, "return after submission")
	namespace := flags.String("namespace", "", "account or organization to bill")
	var env repeatedFlag
	var secrets repeatedFlag
	flags.Var(&env, "env", "KEY=VALUE")
	flags.Var(&secrets, "secret", "secret name")
	if err := parseComputeFlags(flags, args); err != nil {
		return 0, err
	}
	if flags.NArg() == 0 {
		return 0, usageError{"usage: hi compute run [options] <script.py> [-- args...]\n" +
			"       hi compute run [options] <image> -- <command> [args...]"}
	}

	request := runRequest{
		highMem:   *highMem,
		max:       *maxLifetime,
		detach:    *detach,
		namespace: *namespace,
		secrets:   secrets,
	}
	target, rest := flags.Arg(0), flags.Args()[1:]
	if len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	if strings.HasSuffix(target, ".py") {
		if _, err := os.Stat(target); err != nil {
			return 0, fmt.Errorf("script %s: %w", target, err)
		}
		request.script, request.args = target, rest
	} else {
		if len(rest) == 0 {
			return 0, usageError{"give the command to run in the image after --, e.g. `hi compute run python:3.12 -- python -c 'print(1)'`"}
		}
		request.image, request.command = target, rest
	}
	for _, entry := range env {
		if key, _, ok := strings.Cut(entry, "="); !ok || key == "" {
			return 0, usageError{fmt.Sprintf("invalid --env %q; use KEY=VALUE", entry)}
		}
	}
	request.env = env

	provider, err := resolveProvider(*on, *gpu)
	if err != nil {
		return 0, err
	}
	if err := provider.validateRun(request); err != nil {
		return 0, err
	}
	if request.hardware, err = resolveHardware(provider, *gpu); err != nil {
		return 0, err
	}
	if err := validateLifetime(provider, request.max); err != nil {
		return 0, err
	}
	if *name != "" {
		if request.name, err = chooseInstanceName(*name, request.hardware.name); err != nil {
			return 0, err
		}
	}

	what := request.script
	if what == "" {
		what = request.image
	}
	fmt.Fprintf(stdout, "Run %s on %s %s (%s), stopping after %s.\n",
		what, provider.name(), request.hardware.name, request.hardware.rate, formatDuration(request.max))
	if *dryRun {
		fmt.Fprintf(stdout, "Would run: %s\n", strings.Join(provider.runCommand(request), " "))
		return 0, nil
	}
	if err := requireStatus(provider); err != nil {
		return 0, err
	}
	if request.hardware.paid {
		if err := confirm(stdin, stdout, *yes, "Start it?"); err != nil {
			return 0, err
		}
	}
	return provider.runJob(request, stdin, stdout, stderr)
}

func computeWaitCommand(args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 {
		return 0, usageError{"usage: hi compute wait <name>..."}
	}
	worst := 0
	for _, argument := range args {
		provider, name, err := findInstance(argument)
		if err != nil {
			return 0, err
		}
		code, err := provider.wait(name, stdout, stderr)
		if err != nil {
			return 0, err
		}
		if code != 0 {
			worst = code
		}
	}
	return worst, nil
}

type repeatedFlag []string

func (r *repeatedFlag) String() string     { return strings.Join(*r, ",") }
func (r *repeatedFlag) Set(v string) error { *r = append(*r, v); return nil }

// ---------------------------------------------------------------------------
// lifetime watcher

func spawnComputeWatcher(name string) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	directory, err := computeStateDirectory()
	if err != nil {
		return 0, err
	}
	logFile, err := os.OpenFile(filepath.Join(directory, name+".watch.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer logFile.Close()

	command := exec.Command(executable, "compute", "__watch", name)
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return 0, err
	}
	pid := command.Process.Pid
	return pid, command.Process.Release()
}

// watchComputeInstance stops an instance at its deadline. It compares wall
// clock time on every tick so a suspended laptop still stops it on resume.
func watchComputeInstance(name string, stdout, stderr io.Writer) error {
	for {
		records, err := loadComputeRecords()
		if err != nil {
			return err
		}
		record, ok := records[name]
		if !ok {
			return nil
		}
		if !computeNow().Before(record.Deadline) {
			provider, err := providerByName(record.Provider)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s: %s reached its maximum lifetime; stopping it\n",
				computeNow().Format(time.RFC3339), name)
			return stopInstance(provider, name, stdout, stderr)
		}
		time.Sleep(watchInterval)
	}
}

// ---------------------------------------------------------------------------
// local state

func computeStateDirectory() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	directory := filepath.Join(base, "hi", "compute")
	return directory, os.MkdirAll(directory, 0o700)
}

func computeStatePath() (string, error) {
	directory, err := computeStateDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "instances.json"), nil
}

func loadComputeRecords() (map[string]computeRecord, error) {
	path, err := computeStatePath()
	if err != nil {
		return nil, err
	}
	records := map[string]computeRecord{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return records, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return records, nil
}

func writeComputeRecords(records map[string]computeRecord) error {
	path, err := computeStatePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func saveComputeRecord(record computeRecord) error {
	records, err := loadComputeRecords()
	if err != nil {
		return err
	}
	records[record.Name] = record
	return writeComputeRecords(records)
}

func removeComputeRecord(name string) error {
	records, err := loadComputeRecords()
	if err != nil {
		return err
	}
	if _, ok := records[name]; !ok {
		return nil
	}
	delete(records, name)
	return writeComputeRecords(records)
}

// ---------------------------------------------------------------------------
// helpers

func newComputeFlags(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet("hi compute "+name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}

// parseInterspersedFlags accepts flags before and after positional
// arguments, as in `hi compute logs qwen -n 100`.
func parseInterspersedFlags(flags *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := parseComputeFlags(flags, args); err != nil {
			return nil, err
		}
		args = flags.Args()
		if len(args) == 0 {
			return positional, nil
		}
		if args[0] == "--" {
			return append(positional, args[1:]...), nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

func parseComputeFlags(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return usageError{"see `hi compute help`"}
		}
		return usageError{err.Error()}
	}
	return nil
}

func isTerminal(r io.Reader) bool {
	file, ok := r.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

// confirm asks a yes/no question. Without a terminal it never prompts and
// requires --yes instead.
func confirm(stdin io.Reader, stdout io.Writer, yes bool, question string) error {
	if yes {
		return nil
	}
	if !isTerminal(stdin) {
		return errors.New("confirmation needed; rerun with --yes")
	}
	fmt.Fprintf(stdout, "%s [y/N] ", question)
	answer, err := readLine(stdin)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if answer = strings.ToLower(strings.TrimSpace(answer)); answer == "y" || answer == "yes" {
		return nil
	}
	return errors.New("cancelled")
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second)/time.Second))
	}
	d = d.Round(time.Minute)
	hours := int(d / time.Hour)
	minutes := int((d % time.Hour) / time.Minute)
	switch {
	case hours > 0 && minutes > 0:
		return fmt.Sprintf("%dh%02dm", hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
