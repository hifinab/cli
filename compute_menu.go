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
// prints the equivalent command so the menu also teaches the flags.
func computeMenu(stdin io.Reader, stdout, stderr io.Writer) error {
	for {
		fmt.Fprintln(stdout)
		listed, err := listInstances(stdout, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "hi: %v\n", err)
		}
		if len(listed) > 0 {
			fmt.Fprintln(stdout, "Running:")
			for _, item := range listed {
				line := fmt.Sprintf("  %s/%s  %s", item.provider, item.instance.name, item.instance.hardware)
				if item.record != nil {
					line += "  stops in " + formatDuration(item.record.Deadline.Sub(computeNow()))
				}
				fmt.Fprintln(stdout, line)
			}
			fmt.Fprintln(stdout)
		}

		choice, err := menuChoice(stdin, stdout, "What do you want to do?", []string{
			"Start an instance",
			"Open a shell on an instance",
			"Forward a port to this machine",
			"Stop an instance",
			"Run a Python script to completion",
			"Serve a model and tunnel its API here",
			"Show hardware and balance",
			"Quit",
		})
		if err != nil {
			return nil
		}

		var actionErr error
		switch choice {
		case 0:
			actionErr = menuStart(stdin, stdout, stderr)
		case 1:
			actionErr = menuWithInstance(listed, stdin, stdout, func(name string) error {
				fmt.Fprintf(stdout, "$ hi compute ssh %s\n", name)
				return computeSSHCommand([]string{name}, stdin, stdout, stderr)
			})
		case 2:
			actionErr = menuWithInstance(listed, stdin, stdout, func(name string) error {
				remote, err := ask(stdin, stdout, "Port on the instance", "8000")
				if err != nil {
					return err
				}
				local, err := ask(stdin, stdout, "Local port", remote)
				if err != nil {
					return err
				}
				ports := remote
				if local != remote {
					ports += ":" + local
				}
				fmt.Fprintf(stdout, "$ hi compute tunnel %s %s\n", name, ports)
				return computeTunnelCommand([]string{name, ports}, stdin, stdout, stderr)
			})
		case 3:
			actionErr = menuWithInstance(listed, stdin, stdout, func(name string) error {
				if err := confirm(stdin, stdout, false, fmt.Sprintf("Stop %s?", name)); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "$ hi compute stop %s\n", name)
				return computeStopCommand([]string{name}, stdin, stdout, stderr)
			})
		case 4:
			actionErr = menuRun(stdin, stdout, stderr)
		case 5:
			actionErr = menuServe(stdin, stdout, stderr)
		case 6:
			actionErr = computeHardwareCommand(nil, stdout, stderr)
		default:
			return nil
		}
		if actionErr != nil && !errors.Is(actionErr, errMenuBack) {
			fmt.Fprintf(stderr, "hi: %v\n", actionErr)
		}
	}
}

func menuStart(stdin io.Reader, stdout, stderr io.Writer) error {
	provider, err := menuProvider(stdin, stdout)
	if err != nil {
		return err
	}
	hardware, err := menuHardware(provider, stdin, stdout)
	if err != nil {
		return err
	}
	name, err := chooseInstanceName("", hardware.name)
	if err != nil {
		return err
	}
	if name, err = ask(stdin, stdout, "Name", name); err != nil {
		return err
	}
	lifetime, err := ask(stdin, stdout, "Stop automatically after", formatDuration(defaultInstanceMax))
	if err != nil {
		return err
	}

	args := []string{"--on", provider.name(), "--gpu", hardware.name, "--name", name, "--max", lifetime}
	if provider.name() == "colab" && (hardware.name == "cpu" || hardware.name == "T4" || hardware.name == "A100") {
		if confirm(stdin, stdout, false, "Request a high-RAM machine?") == nil {
			args = append(args, "--high-mem")
		}
	}
	fmt.Fprintf(stdout, "$ hi compute up %s\n", strings.Join(args, " "))
	return computeUpCommand(args, stdin, stdout, stderr)
}

func menuRun(stdin io.Reader, stdout, stderr io.Writer) error {
	provider, err := menuProvider(stdin, stdout)
	if err != nil {
		return err
	}
	script, err := ask(stdin, stdout, "Python script", "")
	if err != nil || script == "" {
		return errMenuBack
	}
	hardware, err := menuHardware(provider, stdin, stdout)
	if err != nil {
		return err
	}
	lifetime, err := ask(stdin, stdout, "Stop after", formatDuration(defaultRunMax))
	if err != nil {
		return err
	}
	args := []string{"--on", provider.name(), "--gpu", hardware.name, "--max", lifetime, script}
	fmt.Fprintf(stdout, "$ hi compute run %s\n", strings.Join(args, " "))
	code, err := computeRunCommand(args, stdin, stdout, stderr)
	if err == nil && code != 0 {
		return fmt.Errorf("%s exited with status %d", script, code)
	}
	return err
}

func menuServe(stdin io.Reader, stdout, stderr io.Writer) error {
	provider, err := menuProvider(stdin, stdout)
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
	choice, err := menuChoice(stdin, stdout, "Model:", labels)
	if err != nil {
		return errMenuBack
	}

	args := []string{"--on", provider.name()}
	model := ""
	if choice < len(names) {
		model = names[choice]
	} else {
		if model, err = ask(stdin, stdout, "Repository (owner/Model-GGUF)", ""); err != nil || model == "" {
			return errMenuBack
		}
		quant, err := ask(stdin, stdout, "Quant", "Q4_K_M")
		if err != nil {
			return err
		}
		args = append(args, "--quant", quant)
		hardware, err := menuHardware(provider, stdin, stdout)
		if err != nil {
			return err
		}
		args = append(args, "--gpu", hardware.name)
	}
	lifetime, err := ask(stdin, stdout, "Stop automatically after", formatDuration(defaultInstanceMax))
	if err != nil {
		return err
	}
	port, err := ask(stdin, stdout, "Local port for the API", fmt.Sprint(serveLocalPort))
	if err != nil {
		return err
	}
	args = append(args, "--max", lifetime, "--port", port, model)
	fmt.Fprintf(stdout, "$ hi compute serve %s\n", strings.Join(args, " "))
	return computeServeCommand(args, stdin, stdout, stderr)
}

// menuProvider offers the providers that are ready, and skips the question
// when only one is.
func menuProvider(stdin io.Reader, stdout io.Writer) (computeProvider, error) {
	var ready []computeProvider
	for _, provider := range computeProviders {
		if status := provider.check(); status.installed && status.signedIn {
			ready = append(ready, provider)
		}
	}
	switch len(ready) {
	case 0:
		return nil, errors.New("no provider is ready; run `hi compute providers`")
	case 1:
		return ready[0], nil
	}
	labels := make([]string, len(ready))
	for i, provider := range ready {
		labels[i] = provider.name()
	}
	choice, err := menuChoice(stdin, stdout, "Provider:", labels)
	if err != nil {
		return nil, errMenuBack
	}
	return ready[choice], nil
}

func menuHardware(provider computeProvider, stdin io.Reader, stdout io.Writer) (computeHardware, error) {
	options, err := provider.hardware()
	if err != nil {
		return computeHardware{}, err
	}
	labels := make([]string, len(options))
	for i, hardware := range options {
		labels[i] = fmt.Sprintf("%-5s %-4s %-11s %s", hardware.name, hardware.kind, hardware.memory, hardware.rate)
	}
	choice, err := menuChoice(stdin, stdout, "Hardware:", labels)
	if err != nil {
		return computeHardware{}, errMenuBack
	}
	return options[choice], nil
}

func menuWithInstance(listed []listedInstance, stdin io.Reader, stdout io.Writer, action func(string) error) error {
	switch len(listed) {
	case 0:
		fmt.Fprintln(stdout, "No instances are running.")
		return errMenuBack
	case 1:
		return action(listed[0].instance.name)
	}
	labels := make([]string, len(listed))
	for i, item := range listed {
		labels[i] = fmt.Sprintf("%s/%s  %s", item.provider, item.instance.name, item.instance.hardware)
	}
	choice, err := menuChoice(stdin, stdout, "Instance:", labels)
	if err != nil {
		return errMenuBack
	}
	return action(listed[choice].instance.name)
}

// menuChoice shows numbered options and returns the chosen index. An empty
// answer or end of input goes back.
func menuChoice(stdin io.Reader, stdout io.Writer, prompt string, options []string) (int, error) {
	for {
		fmt.Fprintln(stdout, prompt)
		for i, option := range options {
			fmt.Fprintf(stdout, "  %d) %s\n", i+1, option)
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
