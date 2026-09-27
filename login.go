package main

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
)

// login delegates to each provider's own sign-in; hi never handles the token.
func login(provider string, stdin io.Reader, stdout, stderr io.Writer) error {
	switch provider {
	case "hf":
		hf, err := exec.LookPath("hf")
		if err != nil {
			return errors.New("the hf CLI is not installed; run `hi install`, or `curl -LsSf https://hf.co/cli/install.sh | bash`")
		}
		if err := runInteractive(stdin, stdout, stderr, hf, "auth", "login"); err != nil {
			return fmt.Errorf("hf auth login: %w", err)
		}
		return runInteractive(stdin, stdout, stderr, hf, "auth", "whoami")
	case "colab":
		colab, err := colabBinary()
		if err != nil {
			return err
		}
		// Any authenticated command starts Colab's browser sign-in.
		return runInteractive(stdin, stdout, stderr, colab, "usage")
	default:
		return usageError{fmt.Sprintf("unknown provider %q; use hf or colab", provider)}
	}
}
