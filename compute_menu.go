package main

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

var errMenuBack = errors.New("back")

// computeMenu is the guided flow behind a bare `hi compute`. Every action
// shows the equivalent command so the menu also teaches the flags.
func computeMenu(stdin io.Reader, stdout, stderr io.Writer) error {
	return runMenu(newMenuUI(stdin, stdout), stdin, stdout, stderr)
}

func runMenu(ui menuUI, stdin io.Reader, stdout, stderr io.Writer) error {
	for {
		var listed []listedInstance
		var listErr error
		ui.busy("Checking what is running…", func() { listed, listErr = listInstances(stdout, stderr) })
		if listErr != nil {
			ui.failure(listErr)
		}
		ui.header(listed)

		choice, err := ui.choose("What do you want to do?", []string{
			"▶  Start an instance",
			"⌁  Open a shell on an instance",
			"⇄  Forward a port to this machine",
			"■  Stop an instance",
			"◆  Run a Python script to completion",
			"✦  Serve a model and tunnel its API here",
			"≡  Show hardware and balance",
			"✕  Quit",
		}, false)
		if err != nil {
			return nil
		}

		var actionErr error
		switch choice {
		case 0:
			actionErr = menuStart(ui, stdin, stdout, stderr)
		case 1:
			actionErr = menuWithInstance(ui, listed, func(name string) error {
				ui.command("hi compute ssh " + name)
				return computeSSHCommand([]string{name}, stdin, stdout, stderr)
			})
		case 2:
			actionErr = menuWithInstance(ui, listed, func(name string) error {
				remote, err := ui.input("Port on the instance", "8000", validPort)
				if err != nil {
					return err
				}
				local, err := ui.input("Port on this machine", remote, validPort)
				if err != nil {
					return err
				}
				ports := remote
				if local != remote {
					ports += ":" + local
				}
				ui.command("hi compute tunnel " + name + " " + ports)
				return computeTunnelCommand([]string{name, ports}, stdin, stdout, stderr)
			})
		case 3:
			actionErr = menuWithInstance(ui, listed, func(name string) error {
				yes, err := ui.confirm(fmt.Sprintf("Stop %s?", name), "", false)
				if err != nil || !yes {
					return errMenuBack
				}
				ui.command("hi compute stop " + name)
				return computeStopCommand([]string{name}, stdin, stdout, stderr)
			})
		case 4:
			actionErr = menuRun(ui, stdin, stdout, stderr)
		case 5:
			actionErr = menuServe(ui, stdin, stdout, stderr)
		case 6:
			ui.command("hi compute hardware")
			actionErr = computeHardwareCommand(nil, stdout, stderr)
		default:
			return nil
		}
		if actionErr != nil && !errors.Is(actionErr, errMenuBack) {
			ui.failure(actionErr)
		}
	}
}

func validPort(value string) error {
	_, err := parsePort(value)
	return err
}

func validLifetimeAnswer(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	_, err := parseLifetime(value)
	return err
}

// askLifetime asks how long a machine may run. A bare number is hours; an
// empty answer means no limit, which is confirmed before starting.
func askLifetime(ui menuUI) (string, error) {
	answer, err := ui.input("Time limit in hours (e.g. 1, 1.5, 30m; empty for no limit)", "", validLifetimeAnswer)
	if err != nil {
		return "", err
	}
	if answer == "" {
		return "none", nil
	}
	return answer, nil
}

// confirmStart shows what will start and asks before spending money or
// removing the time limit. It returns whether to go ahead.
func confirmStart(ui menuUI, provider computeProvider, hardware computeHardware, namespace, lifetime, command string) (bool, error) {
	unlimited := lifetime == "none"
	if !hardware.paid && !unlimited {
		return true, nil
	}
	lines := []string{
		fmt.Sprintf("Provider   %s", provider.name()),
		fmt.Sprintf("Hardware   %s (%s)", hardware.name, hardware.rate),
	}
	if account := strings.TrimPrefix(billedSuffix(provider, namespace), ", billed to "); account != "" {
		lines = append(lines, "Billed to  "+account)
	}
	if unlimited {
		lines = append(lines, "Limit      none")
	} else {
		duration, _ := parseLifetime(lifetime)
		lines = append(lines, "Limit      "+formatDuration(duration))
	}
	lines = append(lines, "Command    "+command)
	title := "Start it?"
	if unlimited {
		lines = append(lines, "",
			"Warning: no time limit. It keeps running, and costing money, until you",
			"stop it, even if you close this terminal or forget about it.")
		if limit := provider.maxLifetime(); limit > 0 {
			lines = append(lines, fmt.Sprintf("(%s still ends it after %s.)", provider.name(), formatDuration(limit)))
		}
		title = "Start it with no time limit?"
	}
	return ui.confirm(title, strings.Join(lines, "\n"), unlimited)
}

func menuStart(ui menuUI, stdin io.Reader, stdout, stderr io.Writer) error {
	provider, err := menuProvider(ui, stdin, stdout, stderr)
	if err != nil {
		return err
	}
	hardware, err := menuHardware(ui, provider)
	if err != nil {
		return err
	}
	name, err := chooseInstanceName("", hardware.name)
	if err != nil {
		return err
	}
	if name, err = ui.input("Name", name, func(value string) error {
		if !validInstanceName(value) {
			return fmt.Errorf("use 1-32 lowercase letters, digits, or hyphens, starting with a letter")
		}
		return nil
	}); err != nil {
		return err
	}
	lifetime, err := askLifetime(ui)
	if err != nil {
		return err
	}

	args := []string{"--on", provider.name(), "--gpu", hardware.name, "--name", name, "--max", lifetime}
	if provider.name() == "colab" && (hardware.name == "cpu" || hardware.name == "T4" || hardware.name == "A100") {
		if yes, err := ui.confirm("Request a high-RAM machine?", "", false); err == nil && yes {
			args = append(args, "--high-mem")
		}
	}
	command := "hi compute up " + strings.Join(args, " ")
	if ok, err := confirmStart(ui, provider, hardware, "", lifetime, command); err != nil || !ok {
		ui.note("Nothing was started.")
		return errMenuBack
	}
	ui.command(command)
	return computeUpCommand(append(args, "--yes"), stdin, stdout, stderr)
}

func menuRun(ui menuUI, stdin io.Reader, stdout, stderr io.Writer) error {
	provider, err := menuProvider(ui, stdin, stdout, stderr)
	if err != nil {
		return err
	}
	script, err := ui.input("Python script to run", "", func(value string) error {
		if value == "" {
			return errors.New("enter the path to a .py file")
		}
		if !strings.HasSuffix(value, ".py") {
			return errors.New("the script must be a .py file")
		}
		return nil
	})
	if err != nil || script == "" {
		return errMenuBack
	}
	hardware, err := menuHardware(ui, provider)
	if err != nil {
		return err
	}
	lifetime, err := askLifetime(ui)
	if err != nil {
		return err
	}
	args := []string{"--on", provider.name(), "--gpu", hardware.name, "--max", lifetime, script}
	command := "hi compute run " + strings.Join(args, " ")
	if ok, err := confirmStart(ui, provider, hardware, "", lifetime, command); err != nil || !ok {
		ui.note("Nothing was started.")
		return errMenuBack
	}
	ui.command(command)
	code, err := computeRunCommand(append([]string{"--yes"}, args...), stdin, stdout, stderr)
	if err == nil && code != 0 {
		return fmt.Errorf("%s exited with status %d", script, code)
	}
	return err
}

func menuServe(ui menuUI, stdin io.Reader, stdout, stderr io.Writer) error {
	provider, err := menuProvider(ui, stdin, stdout, stderr)
	if err != nil {
		return err
	}
	names := serveRecipeNames()
	labels := make([]string, 0, len(names)+1)
	for _, name := range names {
		recipe := serveRecipes[name]
		labels = append(labels, fmt.Sprintf("%s  (%s:%s on %s)", name, recipe.repo, recipe.quant, recipe.hardware[provider.name()]))
	}
	labels = append(labels, "Another GGUF model from Hugging Face")
	choice, err := ui.choose("Model", labels, false)
	if err != nil {
		return errMenuBack
	}

	args := []string{"--on", provider.name()}
	model := ""
	var hardware computeHardware
	if choice < len(names) {
		model = names[choice]
		if hardware, err = resolveHardware(provider, serveRecipes[model].hardware[provider.name()]); err != nil {
			return err
		}
	} else {
		if model, err = ui.input("Repository (owner/Model-GGUF)", "", func(value string) error {
			if owner, repo, ok := strings.Cut(value, "/"); !ok || owner == "" || repo == "" {
				return errors.New("use owner/Model-GGUF")
			}
			return nil
		}); err != nil || model == "" {
			return errMenuBack
		}
		quant, err := ui.input("Quant", "Q4_K_M", nil)
		if err != nil {
			return err
		}
		args = append(args, "--quant", quant)
		if hardware, err = menuHardware(ui, provider); err != nil {
			return err
		}
		args = append(args, "--gpu", hardware.name)
	}
	lifetime, err := askLifetime(ui)
	if err != nil {
		return err
	}
	port, err := ui.input("Local port for the API", strconv.Itoa(serveLocalPort), validPort)
	if err != nil {
		return err
	}
	args = append(args, "--max", lifetime, "--port", port, model)
	command := "hi compute serve " + strings.Join(args, " ")
	if ok, err := confirmStart(ui, provider, hardware, "", lifetime, command); err != nil || !ok {
		ui.note("Nothing was started.")
		return errMenuBack
	}
	ui.command(command)
	return computeServeCommand(append([]string{"--yes"}, args...), stdin, stdout, stderr)
}

// menuProvider offers every provider with its sign-in state, and signs in
// to the chosen one first when needed.
func menuProvider(ui menuUI, stdin io.Reader, stdout, stderr io.Writer) (computeProvider, error) {
	labels := make([]string, len(computeProviders))
	for i, provider := range computeProviders {
		labels[i] = providerLabel(provider.name())
		if status := provider.check(); !status.installed || !status.signedIn {
			labels[i] += "  (not signed in)"
		}
	}
	choice, err := ui.choose("Provider", labels, false)
	if err != nil {
		return nil, errMenuBack
	}
	provider := computeProviders[choice]
	if status := provider.check(); status.installed && status.signedIn {
		return provider, nil
	}
	if err := menuSignIn(ui, provider, stdin, stdout, stderr); err != nil {
		return nil, err
	}
	if status := provider.check(); !status.installed || !status.signedIn {
		return nil, fmt.Errorf("%s is still not ready; %s", provider.name(), strings.Join(provider.check().hints, "; "))
	}
	return provider, nil
}

// menuSignIn signs in to a provider from inside the menu.
func menuSignIn(ui menuUI, provider computeProvider, stdin io.Reader, stdout, stderr io.Writer) error {
	switch runpod := provider.(type) {
	case *runpodProvider:
		ui.note("RunPod needs an API key with read and write access. Create one at " + runpodKeysURL)
		key, err := ui.secret("RunPod API key")
		if err != nil || key == "" {
			return errMenuBack
		}
		var path string
		ui.busy("Checking the key with RunPod…", func() { path, err = runpod.signIn(key) })
		if err != nil {
			return err
		}
		ui.note("Saved in " + path + ", readable only by you.")
		return nil
	}
	if status := provider.check(); !status.installed {
		return fmt.Errorf("%s is not installed; %s", provider.name(), strings.Join(status.hints, "; "))
	}
	ui.command("hi login " + provider.name())
	return login(provider.name(), stdin, stdout, stderr)
}

func providerLabel(name string) string {
	switch name {
	case "colab":
		return "colab  Google Colab, prepaid compute units"
	case "hf":
		return "hf     Hugging Face Jobs, billed per minute"
	case "runpod":
		return "runpod RunPod pods, pay as you go"
	}
	return name
}

func menuHardware(ui menuUI, provider computeProvider) (computeHardware, error) {
	var options []computeHardware
	var err error
	ui.busy("Fetching "+provider.name()+" hardware…", func() { options, err = provider.hardware() })
	if err != nil {
		return computeHardware{}, err
	}
	nameWidth, memoryWidth := 0, 0
	for _, hardware := range options {
		nameWidth = max(nameWidth, len(hardware.name))
		memoryWidth = max(memoryWidth, len(hardware.memory))
	}
	labels := make([]string, len(options))
	for i, hardware := range options {
		labels[i] = fmt.Sprintf("%-*s  %-4s %-*s  %s",
			nameWidth, hardware.name, hardware.kind, memoryWidth, hardware.memory, hardware.rate)
	}
	choice, err := ui.choose("Hardware", labels, len(options) > 10)
	if err != nil {
		return computeHardware{}, errMenuBack
	}
	return options[choice], nil
}

func menuWithInstance(ui menuUI, listed []listedInstance, action func(string) error) error {
	switch len(listed) {
	case 0:
		ui.note("No instances are running.")
		return errMenuBack
	case 1:
		return action(listed[0].instance.name)
	}
	labels := make([]string, len(listed))
	for i, item := range listed {
		labels[i] = fmt.Sprintf("%s/%s  %s  %s", item.provider, item.instance.name, item.instance.hardware, limitText(item))
	}
	choice, err := ui.choose("Instance", labels, false)
	if err != nil {
		return errMenuBack
	}
	return action(listed[choice].instance.name)
}

// menuChoice shows numbered options and returns the chosen index. An empty
// answer or end of input goes back.
func menuChoice(stdin io.Reader, stdout io.Writer, prompt string, options []string) (int, error) {
	for {
		if strings.HasSuffix(prompt, "?") {
			fmt.Fprintln(stdout, prompt)
		} else {
			fmt.Fprintln(stdout, prompt+":")
		}
		width := len(strconv.Itoa(len(options)))
		for i, option := range options {
			fmt.Fprintf(stdout, "  %*d) %s\n", width, i+1, option)
		}
		fmt.Fprint(stdout, "> ")
		answer, err := readLine(stdin)
		answer = strings.TrimSpace(answer)
		if answer == "" && err != nil {
			return 0, errMenuBack
		}
		if answer == "" || answer == "q" {
			return 0, errMenuBack
		}
		if number, convErr := strconv.Atoi(answer); convErr == nil && number >= 1 && number <= len(options) {
			return number - 1, nil
		}
		fmt.Fprintf(stdout, "Choose a number from 1 to %d.\n", len(options))
	}
}

func ask(stdin io.Reader, stdout io.Writer, prompt, fallback string) (string, error) {
	if fallback != "" {
		fmt.Fprintf(stdout, "%s [%s]: ", prompt, fallback)
	} else {
		fmt.Fprintf(stdout, "%s: ", prompt)
	}
	answer, err := readLine(stdin)
	answer = strings.TrimSpace(answer)
	if answer == "" {
		if err != nil && fallback == "" {
			return "", errMenuBack
		}
		return fallback, nil
	}
	return answer, nil
}
