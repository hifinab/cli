package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/term"
)

const netBirdSetupKeyEnv = "HI_NETBIRD_SETUP_KEY"

func connectNetBird(setupKeyFile string, stdin io.Reader, stdout, stderr io.Writer) error {
	netbird, err := exec.LookPath("netbird")
	if err != nil {
		return errors.New("netbird is not installed; run `hi install` first")
	}

	setupKey, err := readNetBirdSetupKey(setupKeyFile, stdin, stdout)
	if err != nil {
		return err
	}
	defer clear(setupKey)

	currentHostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("read current hostname: %w", err)
	}
	deviceHostname, err := promptNetBirdHostname(stdin, stdout, currentHostname)
	if err != nil {
		return err
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	keyPath, err := writeTemporarySetupKey(setupKey)
	if err != nil {
		return err
	}
	args := []string{"up", "--setup-key-file", keyPath, "--hostname", deviceHostname}
	if err := runNetBirdEnrollment(netbird, args, stdin, stdout, stderr, signals, keyPath); err != nil {
		return fmt.Errorf("connect NetBird: %w", err)
	}
	return nil
}

func readNetBirdSetupKey(path string, stdin io.Reader, stdout io.Writer) ([]byte, error) {
	if path != "" {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open setup-key file: %w", err)
		}
		defer file.Close()

		info, err := file.Stat()
		if err != nil {
			return nil, fmt.Errorf("inspect setup-key file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("setup-key file must be a regular file")
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("setup-key file must not be accessible by group or other users; run `chmod 600 <path>`")
		}
		key, err := io.ReadAll(file)
		if err != nil {
			return nil, fmt.Errorf("read setup-key file: %w", err)
		}
		return nonemptySetupKey(key)
	}

	if value, present := os.LookupEnv(netBirdSetupKeyEnv); present {
		return nonemptySetupKey([]byte(value))
	}

	terminal, ok := stdin.(*os.File)
	if !ok || !term.IsTerminal(int(terminal.Fd())) {
		return nil, fmt.Errorf("setup key requires a terminal; set %s or use `hi net --setup-key-file <path>`", netBirdSetupKeyEnv)
	}
	fmt.Fprint(stdout, "NetBird setup key: ")
	key, err := term.ReadPassword(int(terminal.Fd()))
	fmt.Fprintln(stdout)
	if err != nil {
		return nil, fmt.Errorf("read NetBird setup key: %w", err)
	}
	return nonemptySetupKey(key)
}

func nonemptySetupKey(key []byte) ([]byte, error) {
	key = bytes.TrimSpace(key)
	if len(key) == 0 {
		return nil, errors.New("NetBird setup key cannot be blank")
	}
	return key, nil
}

func writeTemporarySetupKey(key []byte) (string, error) {
	file, err := os.CreateTemp("", "hi-netbird-key-*")
	if err != nil {
		return "", fmt.Errorf("create temporary setup-key file: %w", err)
	}
	path := file.Name()
	remove := func() {
		file.Close()
		os.Remove(path)
	}
	if err := file.Chmod(0o600); err != nil {
		remove()
		return "", fmt.Errorf("secure temporary setup-key file: %w", err)
	}
	if _, err := file.Write(key); err != nil {
		remove()
		return "", fmt.Errorf("write temporary setup-key file: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close temporary setup-key file: %w", err)
	}
	return path, nil
}

func runNetBirdEnrollment(
	netbird string,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	signals <-chan os.Signal,
	keyPath string,
) error {
	var removeErr error
	var removeOnce sync.Once
	removeKey := func() error {
		removeOnce.Do(func() {
			removeErr = os.Remove(keyPath)
		})
		return removeErr
	}

	select {
	case received := <-signals:
		if err := removeKey(); err != nil {
			return fmt.Errorf("remove temporary setup-key file after %s: %w", received, err)
		}
		return fmt.Errorf("interrupted by %s", received)
	default:
	}

	command := exec.Command(netbird, args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = netBirdEnvironment()
	if err := command.Start(); err != nil {
		cleanupErr := removeKey()
		if cleanupErr != nil {
			return fmt.Errorf("start NetBird: %v; remove temporary setup-key file: %w", err, cleanupErr)
		}
		return fmt.Errorf("start NetBird: %w", err)
	}

	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()

	var runErr error
	select {
	case runErr = <-wait:
	case received := <-signals:
		removeKey()
		_ = command.Process.Signal(received)
		<-wait
		runErr = fmt.Errorf("interrupted by %s", received)
	}
	if cleanupErr := removeKey(); cleanupErr != nil {
		if runErr != nil {
			return fmt.Errorf("%v; remove temporary setup-key file: %w", runErr, cleanupErr)
		}
		return fmt.Errorf("remove temporary setup-key file: %w", cleanupErr)
	}
	return runErr
}

func runNetBirdLifecycle(action string, stdin io.Reader, stdout, stderr io.Writer) error {
	netbird, err := exec.LookPath("netbird")
	if err != nil {
		return errors.New("netbird is not installed; run `hi install` first")
	}
	run := func(command string) error {
		cmd := exec.Command(netbird, command)
		cmd.Stdin = stdin
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		cmd.Env = netBirdEnvironment()
		return cmd.Run()
	}

	if action == "reconnect" {
		if err := run("down"); err != nil {
			return fmt.Errorf("disconnect NetBird: %w", err)
		}
		if err := run("up"); err != nil {
			return fmt.Errorf("reconnect NetBird: %w", err)
		}
		return nil
	}
	if err := run(action); err != nil {
		return fmt.Errorf("%s NetBird: %w", action, err)
	}
	return nil
}

func netBirdEnvironment() []string {
	prefix := netBirdSetupKeyEnv + "="
	environment := os.Environ()
	filtered := environment[:0]
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func promptNetBirdHostname(r io.Reader, w io.Writer, current string) (string, error) {
	fmt.Fprintln(w, "NetBird will use this name as the device hostname shown to other peers.")
	fmt.Fprintf(w, "Device hostname [%s]: ", current)
	line, err := readLine(r)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read device hostname: %w", err)
	}

	hostname := strings.TrimSpace(line)
	if hostname == "" {
		return current, nil
	}
	if !validHostname(hostname) {
		return "", fmt.Errorf(
			"invalid device hostname %q; use dot-separated letters, digits, or hyphens (maximum 64 characters)",
			hostname,
		)
	}
	return hostname, nil
}
