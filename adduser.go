package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
)

func addUser(username string, stdin io.Reader, stdout, stderr io.Writer) error {
	if runtime.GOOS != "linux" {
		return errors.New("user creation supports Linux only")
	}
	if os.Geteuid() == 0 {
		return errors.New("run this command as your regular user; it uses sudo when needed")
	}
	if !validUsername(username) {
		return fmt.Errorf("invalid username %q; use 1-32 lowercase letters, digits, hyphens, or underscores, starting with a letter", username)
	}
	if runSuccessful("getent", "passwd", username) {
		return fmt.Errorf("user %q already exists", username)
	}
	for _, group := range []string{"render", "video"} {
		if !runSuccessful("getent", "group", group) {
			return fmt.Errorf("required group %q does not exist", group)
		}
	}

	if err := runInteractive(stdin, stdout, stderr, "sudo", "-v"); err != nil {
		return fmt.Errorf("authenticate with sudo: %w", err)
	}
	if err := runInteractive(stdin, stdout, stderr, "sudo", "adduser", username); err != nil {
		return fmt.Errorf("create user %q: %w", username, err)
	}
	if err := runInteractive(stdin, stdout, stderr, "sudo", "usermod", "-a", "-G", "render,video", username); err != nil {
		return fmt.Errorf("add user %q to GPU groups: %w", username, err)
	}

	fmt.Fprintf(stdout, "\nCreated user %q and added it to the render and video groups.\n", username)
	return nil
}

func runInteractive(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func validUsername(username string) bool {
	if len(username) == 0 || len(username) > 32 || username[0] < 'a' || username[0] > 'z' {
		return false
	}
	for _, char := range username[1:] {
		if (char < 'a' || char > 'z') &&
			(char < '0' || char > '9') &&
			char != '-' && char != '_' {
			return false
		}
	}
	return true
}
