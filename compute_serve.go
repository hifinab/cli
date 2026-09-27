package main

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed scripts/compute/serve-llama-cpp.sh
var serveLlamaCppScript []byte

const (
	serveRemotePort  = 8000
	serveLocalPort   = 8080
	servePollTimeout = 45 * time.Second
	serveReadyWithin = 45 * time.Minute
)

// servePollEvery is shortened in tests.
var servePollEvery = 10 * time.Second

// serveRecipe is a tested model setup. Flags are hardware-specific, so a
// recipe lists the hardware it has been run on.
type serveRecipe struct {
	name     string
	repo     string
	quant    string
	context  int
	alias    string
	args     string
	hardware string
	tested   string
}

var serveRecipes = map[string]serveRecipe{
	"qwen3.8-flash-next": {
		name:    "qwen3.8-flash-next",
		repo:    "unsloth/Qwen3.8-Flash-Next-GGUF",
		quant:   "UD-Q3_K_XL",
		context: 131072,
		alias:   "qwen3.8-flash-next",
		// The 28.8 GB n-gram table stays fully in host RAM; lazy mode would
		// stream it from Colab's slow network disk.
		args:     `-ot per_layer_token_embd\.weight=CPU --lazy-mode off --temp 1.0 --top-p 0.95 --top-k 20 --min-p 0.0`,
		hardware: "G4",
		tested:   "Colab G4, 2026-09-27: ~103 tokens/s, 62.9 GB VRAM at 131k context, ready in ~7 min",
	},
}

func computeServeCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := newComputeFlags("serve", stderr)
	on := flags.String("on", "", "provider")
	gpu := flags.String("gpu", "", "hardware")
	name := flags.String("name", "", "instance name")
	maxLifetime := flags.Duration("max", defaultInstanceMax, "maximum lifetime")
	quant := flags.String("quant", "", "GGUF quant")
	contextSize := flags.Int("ctx", 0, "context length")
	alias := flags.String("alias", "", "model id in the API")
	extra := flags.String("args", "", "extra llama-server arguments")
	localPort := flags.Int("port", serveLocalPort, "local port")
	yes := flags.Bool("yes", false, "skip confirmation")
	dryRun := flags.Bool("dry-run", false, "show without starting")
	if err := parseComputeFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return usageError{"usage: hi compute serve [options] <recipe | owner/repo-GGUF --quant Q>\n" +
			"recipes: " + strings.Join(serveRecipeNames(), ", ")}
	}

	recipe, err := resolveServeRecipe(flags.Arg(0), *quant, *contextSize, *alias, *extra)
	if err != nil {
		return err
	}
	if *gpu == "" {
		*gpu = recipe.hardware
	}
	if *gpu == "" {
		return usageError{"choose hardware with --gpu; see `hi compute hardware`"}
	}
	if _, err := parsePort(strconv.Itoa(*localPort)); err != nil {
		return err
	}

	provider, err := resolveProvider(*on)
	if err != nil {
		return err
	}
	hardware, err := resolveHardware(provider, *gpu)
	if err != nil {
		return err
	}
	if hardware.kind != "GPU" {
		return usageError{fmt.Sprintf("serving needs a GPU; %s is a %s", hardware.name, hardware.kind)}
	}
	if err := validateLifetime(provider, *maxLifetime); err != nil {
		return err
	}
	instance, err := chooseInstanceName(*name, hardware.name)
	if err != nil {
		return err
	}
	if recipe.tested != "" {
		fmt.Fprintf(stdout, "Recipe %s, tested on %s.\n", recipe.name, recipe.tested)
	}

	running := false
	if *name != "" {
		if instances, err := provider.list(); err == nil {
			for _, candidate := range instances {
				running = running || candidate.name == instance
			}
		}
	}
	if running {
		fmt.Fprintf(stdout, "Reusing %s/%s.\n", provider.name(), instance)
	} else {
		request := upRequest{name: instance, hardware: hardware, max: *maxLifetime}
		if err := startInstance(provider, request, *yes, *dryRun, stdin, stdout, stderr); err != nil {
			return err
		}
	}
	if *dryRun {
		fmt.Fprintf(stdout, "Would serve %s:%s on port %d and tunnel it to 127.0.0.1:%d.\n",
			recipe.repo, recipe.quant, serveRemotePort, *localPort)
		return nil
	}

	target, err := provider.ssh(instance)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Starting %s:%s on %s (a fresh machine needs a few minutes to build and download).\n",
		recipe.repo, recipe.quant, instance)
	if err := startRemoteServer(target, recipe, stderr); err != nil {
		return err
	}
	if err := waitForRemoteServer(target, instance, stdout); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "\nOpenAI-compatible API: http://127.0.0.1:%d/v1   model: %s\n", *localPort, recipe.alias)
	return computeTunnelCommand([]string{instance, fmt.Sprintf("%d:%d", serveRemotePort, *localPort)},
		stdin, stdout, stderr)
}

func serveRecipeNames() []string {
	names := make([]string, 0, len(serveRecipes))
	for name := range serveRecipes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// resolveServeRecipe returns a built-in recipe, or an ad hoc one for a
// Hugging Face GGUF repository. Flags override either.
func resolveServeRecipe(argument, quant string, contextSize int, alias, extra string) (serveRecipe, error) {
	recipe, known := serveRecipes[strings.ToLower(argument)]
	if !known {
		owner, repo, ok := strings.Cut(argument, "/")
		if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
			return serveRecipe{}, usageError{fmt.Sprintf(
				"%q is neither a recipe (%s) nor a Hugging Face repository like owner/Model-GGUF",
				argument, strings.Join(serveRecipeNames(), ", "))}
		}
		if quant == "" {
			return serveRecipe{}, usageError{"choose a quant with --quant, such as Q4_K_M"}
		}
		recipe = serveRecipe{name: argument, repo: argument, context: 32768, alias: strings.ToLower(repo)}
	}
	if quant != "" {
		recipe.quant = quant
	}
	if contextSize > 0 {
		recipe.context = contextSize
	}
	if alias != "" {
		recipe.alias = alias
	}
	if extra != "" {
		recipe.args = extra
	}
	for _, value := range []string{recipe.repo, recipe.quant, recipe.alias} {
		if strings.ContainsAny(value, "'\"`$\\ \n") {
			return serveRecipe{}, usageError{fmt.Sprintf("invalid characters in %q", value)}
		}
	}
	return recipe, nil
}

// startRemoteServer uploads the serve script over SSH and starts it detached,
// so a dropped connection never stops setup.
func startRemoteServer(target sshTarget, recipe serveRecipe, stderr io.Writer) error {
	upload := "mkdir -p ~/.hi && cat > ~/.hi/serve-llama-cpp.sh && chmod 700 ~/.hi/serve-llama-cpp.sh"
	if _, err := remoteOutput(target, bytes.NewReader(serveLlamaCppScript), 2*time.Minute, upload); err != nil {
		return fmt.Errorf("upload the serve script: %w", err)
	}
	environment := []string{
		"REPO=" + shellQuote(recipe.repo),
		"QUANT=" + shellQuote(recipe.quant),
		"CTX=" + strconv.Itoa(recipe.context),
		"PORT=" + strconv.Itoa(serveRemotePort),
		"ALIAS=" + shellQuote(recipe.alias),
		"EXTRA_ARGS=" + shellQuote(recipe.args),
	}
	start := "rm -f ~/.hi/state; " + strings.Join(environment, " ") +
		" setsid nohup bash ~/.hi/serve-llama-cpp.sh > /dev/null 2>&1 < /dev/null &"
	if _, err := remoteOutput(target, nil, time.Minute, start); err != nil {
		return fmt.Errorf("start the server: %w", err)
	}
	return nil
}

// waitForRemoteServer polls ~/.hi/state with short SSH calls. A failed poll
// only means no update this time.
func waitForRemoteServer(target sshTarget, instance string, stdout io.Writer) error {
	deadline := computeNow().Add(serveReadyWithin)
	last := ""
	for computeNow().Before(deadline) {
		time.Sleep(servePollEvery)
		output, err := remoteOutput(target, nil, servePollTimeout,
			"cat ~/.hi/state 2>/dev/null; du -sh /content/hi/models ~/.hi/models 2>/dev/null | head -1 | cut -f1")
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimSpace(output), "\n")
		state := strings.TrimSpace(lines[0])
		if state == "" {
			continue
		}
		progress := state
		if len(lines) > 1 && strings.TrimSpace(lines[1]) != "" && !strings.HasPrefix(state, "ready") {
			progress += "  (model on disk: " + strings.TrimSpace(lines[1]) + ")"
		}
		if progress != last {
			fmt.Fprintf(stdout, "  [%s] %s\n", computeNow().Format("15:04:05"), progress)
			last = progress
		}
		if strings.HasPrefix(state, "ready") {
			return nil
		}
		if strings.HasPrefix(state, "failed") {
			return fmt.Errorf("%s; see `hi compute logs %s`", state, instance)
		}
	}
	return fmt.Errorf("the server was not ready after %s; see `hi compute logs %s`",
		formatDuration(serveReadyWithin), instance)
}

func remoteOutput(target sshTarget, stdin io.Reader, timeout time.Duration, command string) (string, error) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		return "", errors.New("ssh is not installed; install openssh-client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := append([]string{"-o", "BatchMode=yes"}, target.options...)
	args = append(args, target.destination, command)
	var output bytes.Buffer
	process := exec.CommandContext(ctx, ssh, args...)
	process.Stdin = stdin
	process.Stdout = &output
	process.Stderr = &output
	if err := process.Run(); err != nil {
		if ctx.Err() != nil {
			return output.String(), fmt.Errorf("no answer within %s", timeout)
		}
		return output.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(output.String()))
	}
	return output.String(), nil
}

func computeLogsCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := newComputeFlags("logs", stderr)
	follow := flags.Bool("follow", false, "keep printing new lines")
	lines := flags.Int("n", 40, "lines per log")
	if err := parseComputeFlags(flags, args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return usageError{"usage: hi compute logs <name> [--follow] [-n lines]"}
	}
	provider, name, err := findInstance(flags.Arg(0))
	if err != nil {
		return err
	}
	target, err := provider.ssh(name)
	if err != nil {
		return err
	}
	tail := fmt.Sprintf("tail -n %d", *lines)
	if *follow {
		tail += " -F"
	}
	command := "cat ~/.hi/state 2>/dev/null; ls ~/.hi/logs/*.log >/dev/null 2>&1 && " + tail +
		" ~/.hi/logs/*.log || echo 'No logs yet.'"
	sshArgs := append(append([]string{}, target.options...), target.destination, command)
	return runSSH(sshArgs, stdin, stdout, stderr)
}
