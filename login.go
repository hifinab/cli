package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"golang.org/x/term"
)

// runpodForLogin is the registered RunPod provider, so a sign-in refreshes
// its cached hardware list.
func runpodForLogin() *runpodProvider {
	for _, provider := range computeProviders {
		if runpod, ok := provider.(*runpodProvider); ok {
			return runpod
		}
	}
	return newRunpodProvider()
}

// shadeformForLogin is the registered Shadeform provider.
func shadeformForLogin() *shadeformProvider {
	for _, provider := range computeProviders {
		if shadeform, ok := provider.(*shadeformProvider); ok {
			return shadeform
		}
	}
	return newShadeformProvider()
}

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
		terminal, ok := stdin.(*os.File)
		if !ok || !term.IsTerminal(int(terminal.Fd())) {
			return fmt.Errorf("`hi login runpod` asks for the key in a terminal; in scripts, set RUNPOD_API_KEY instead")
		}
		fmt.Fprintf(stdout, "Create an API key with read and write access at %s\n", runpodKeysURL)
		fmt.Fprint(stdout, "RunPod API key (paste it; it shows as *): ")
		key, err := readSecret(terminal, stdout)
		fmt.Fprintln(stdout)
		if err != nil {
			return fmt.Errorf("read the API key: %w", err)
		}
		path, err := runpodForLogin().signIn(string(key))
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "RunPod is ready. The key is saved in %s, readable only by you.\n", path)
		if os.Getenv("RUNPOD_API_KEY") != "" {
			fmt.Fprintln(stdout, "Note: RUNPOD_API_KEY is set in this shell and takes precedence over the saved key.")
		}
		return nil
	case "shadeform":
		terminal, ok := stdin.(*os.File)
		if !ok || !term.IsTerminal(int(terminal.Fd())) {
			return fmt.Errorf("`hi login shadeform` asks for the key in a terminal; in scripts, set SHADEFORM_API_KEY instead")
		}
		fmt.Fprintf(stdout, "Create an API key at %s\n", shadeformKeysURL)
		fmt.Fprint(stdout, "Shadeform API key (paste it; it shows as *): ")
		key, err := readSecret(terminal, stdout)
		fmt.Fprintln(stdout)
		if err != nil {
			return fmt.Errorf("read the API key: %w", err)
		}
		path, err := shadeformForLogin().signIn(string(key))
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Shadeform is ready. The key is saved in %s, readable only by you.\n", path)
		if os.Getenv("SHADEFORM_API_KEY") != "" {
			fmt.Fprintln(stdout, "Note: SHADEFORM_API_KEY is set in this shell and takes precedence over the saved key.")
		}
		return nil
	default:
		return usageError{fmt.Sprintf("unknown provider %q; use hf, colab, runpod, or shadeform", provider)}
	}
}
