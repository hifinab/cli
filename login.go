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
	case "runpod":
		if runpodctl, err := exec.LookPath("runpodctl"); err == nil {
			// runpodctl asks for the key and stores it in ~/.runpod/config.toml.
			return runInteractive(stdin, stdout, stderr, runpodctl, "doctor")
		}
		fmt.Fprintf(stdout, "1. Create an API key with read and write access at %s\n", runpodKeysURL)
		fmt.Fprintln(stdout, "2. Put it in your shell profile, e.g. ~/.profile:  export RUNPOD_API_KEY=...")
		fmt.Fprintln(stdout, "   or install runpodctl and run `runpodctl doctor`, which saves it in ~/.runpod/config.toml.")
		fmt.Fprintln(stdout, "3. Check with `hi compute providers`.")
		return nil
	default:
		return usageError{fmt.Sprintf("unknown provider %q; use hf, colab, or runpod", provider)}
	}
}
