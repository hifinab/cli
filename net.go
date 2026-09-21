package main

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
)

func connectNetBird(setupKey string, stdin io.Reader, stdout, stderr io.Writer) error {
	netbird, err := exec.LookPath("netbird")
	if err != nil {
		return errors.New("netbird is not installed; run `hi install` first")
	}
	if err := runInteractive(stdin, stdout, stderr, netbird, "up", "--setup-key", setupKey); err != nil {
		return fmt.Errorf("connect NetBird: %w", err)
	}
	return nil
}
