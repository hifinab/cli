package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stdout)
		return 0
	}

	switch args[0] {
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	case "version", "-v", "--version":
		fmt.Fprintf(stdout, "hi %s\n", version)
		return 0
	case "adduser":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: hi adduser <username>")
			return 2
		}
		if err := addUser(args[1], stdin, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "hi: %v\n", err)
			return 1
		}
		return 0
	case "install":
		return runInstall(args[1:], stdin, stdout, stderr)
	case "uninstall":
		return runUninstall(args[1:], stdin, stdout, stderr)
	case "net":
		if len(args) >= 2 && args[1] == "expose" {
			return runNetExpose(args[2:], stdin, stdout, stderr)
		}
		var err error
		switch {
		case len(args) == 1:
			err = connectNetBird("", stdin, stdout, stderr)
		case len(args) == 2 && (args[1] == "status" || args[1] == "down" || args[1] == "reconnect"):
			err = runNetBirdLifecycle(args[1], stdin, stdout, stderr)
		case len(args) == 3 && args[1] == "--setup-key-file" && args[2] != "":
			err = connectNetBird(args[2], stdin, stdout, stderr)
		default:
			printNetUsage(stderr)
			return 2
		}
		if err != nil {
			fmt.Fprintf(stderr, "hi: %v\n", err)
			return 1
		}
		return 0
	case "update":
		return runUpdate(args[1:], stdin, stdout, stderr)
	case "skill", "skills":
		return runSkill(args[1:], stdin, stdout, stderr)
	case "q":
		return runQ(args[1:], stdin, stdout, stderr)
	case "box":
		return runBox(args[1:], stdin, stdout, stderr)
	case "agent":
		return runAgent(args[1:], stdin, stdout, stderr)
	case "bundle", "bundles":
		return runBundle(args[1:], stdin, stdout, stderr)
	case "shell-init":
		return runShellInit(args[1:], stdout, stderr)
	case "init":
		return runInit(args[1:], stdin, stdout, stderr)
	case "login":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: hi login <hf|colab|runpod|shadeform>")
			return 2
		}
		return exitCode(login(args[1], stdin, stdout, stderr), stderr)
	case "compute":
		return runCompute(args[1:], stdin, stdout, stderr)
	case "data":
		return runData(args[1:], stdin, stdout, stderr)
	case "connect":
		return runConnect(args[1:], stdin, stdout, stderr)
	case "disconnect":
		return runDisconnect(args[1:], stdout, stderr)
	case "server":
		return runServer(args[1:], stdin, stdout, stderr)
	case "verify":
		if len(args) != 2 || args[1] != "strix" {
			fmt.Fprintln(stderr, "usage: hi verify strix")
			return 2
		}
		if result := verifyStrix(stdout); result.failures > 0 {
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "hi: unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `hi prepares Hifin development machines and projects.

Usage:
  hi adduser <name>             Create a user with render and video access
  hi install                    Choose workstation software to install or remove
  hi install <tool>... | --all  Install the named tools, or all of them
  hi install --list             List the tools and whether they are installed
  hi uninstall <tool>...        Remove the named tools
  hi net                        Securely enroll this machine with NetBird
  hi net status                 Show NetBird connection status
  hi net down                   Disconnect NetBird
  hi net reconnect              Reconnect an enrolled NetBird peer
  hi net expose <port>          Put a local service on a temporary public address (hi net expose help)
  hi compute                    Start and use remote GPU machines (Colab, Hugging Face, RunPod, Shadeform)
  hi login <hf|colab|runpod|shadeform>
                                Sign in to a compute provider
  hi connect <server>           Join a hi server that approves and pays for compute
  hi data                       Download the team's Hugging Face datasets, models, and buckets
  hi disconnect                 Leave it; hi compute uses your own keys again
  hi server                     Run the server that brokers compute and serves templates for a team
  hi init [<template> <dir>]    Start a project from a template (python, web, service, pipeline, ml)
  hi q <what you want to do>    Ask an AI model for a shell command, then run, copy, or explain it
  hi q                          Chat with it; hi q --setup chooses the model and sets up the shell
  hi agent [claude|codex] [task]
                                Hand a task to a coding agent in a box and get its report (hi agent help)
  hi bundle ls|show             Bundles for hi agent --bundle: skills and the tools they need
  hi box shell|run              Run a shell or a command in a rootless box with no credentials
  hi skills                     Find, install, and update agent skills from skills.sh (hi skill help);
                                hi skill add hi teaches agents to use hi
  hi verify strix               Check an installed Strix Halo workstation
  hi update [--check]           Update hi to the latest release, and restart a hi server running the old one
  hi version                    Print the installed version
  hi help                       Show this help`)
}

func printNetUsage(w io.Writer) {
	fmt.Fprintln(w, `usage:
  hi net
  hi net --setup-key-file <path>
  hi net status
  hi net down
  hi net reconnect
  hi net expose <port> [--max 1h] [--public | --pin P | --groups G] [--detach]
  hi net expose ls | stop <name>`)
}

func readLine(r io.Reader) (string, error) {
	var line strings.Builder
	var next [1]byte
	for {
		n, err := r.Read(next[:])
		if n == 1 {
			switch next[0] {
			case '\n':
				return line.String(), nil
			case '\r':
			default:
				line.WriteByte(next[0])
			}
		}
		if err != nil {
			return line.String(), err
		}
	}
}

func validHostname(hostname string) bool {
	if len(hostname) == 0 || len(hostname) > 64 {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') &&
				(char < 'A' || char > 'Z') &&
				(char < '0' || char > '9') &&
				char != '-' {
				return false
			}
		}
	}
	return true
}
