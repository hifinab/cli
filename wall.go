package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
)

// A wall screen shows `hi server live --wall` from a tmux session on the
// server box. The session runs as its own account, hi-wall, which holds only
// a viewer key and cannot read the server's state or provider keys. Screens
// attach over SSH with keys that can do nothing but a read-only attach.

const (
	wallUser   = "hi-wall"
	wallHome   = "/var/lib/hi-wall"
	wallBinary = "/usr/local/lib/hi/hi"
)

var wallSessionName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// wallUnit is the systemd unit that keeps one wall session running. Each
// session has its own tmux server, so systemd can follow it.
func wallUnit(session, size string) string {
	create := fmt.Sprintf("/usr/bin/tmux -L wall-%s new-session -d -s %s", session, session)
	// A wall shows only the dashboard: no tmux status bar.
	post := fmt.Sprintf("ExecStartPost=/usr/bin/tmux -L wall-%s set-option -t %s status off\n", session, session)
	if size != "" {
		width, height, _ := strings.Cut(size, "x")
		create += fmt.Sprintf(" -x %s -y %s", width, height)
		post += fmt.Sprintf("ExecStartPost=/usr/bin/tmux -L wall-%s set-option -t %s window-size manual\n", session, session)
	}
	create += " " + wallBinary + " server live --wall"
	return fmt.Sprintf(`[Unit]
Description=hi compute wall dashboard (%[1]s)
After=network-online.target hi-server.service
Wants=network-online.target

[Service]
Type=forking
User=%[2]s
Environment=HOME=%[3]s
ExecStart=%[4]s
%[5]sExecStop=/usr/bin/tmux -L wall-%[1]s kill-session -t %[1]s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, session, wallUser, wallHome, create, post)
}

// wallKeyLine lets a screen's SSH key do one thing: attach read-only.
func wallKeyLine(session, screen, publicKey string) (string, error) {
	fields := strings.Fields(publicKey)
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "ssh-") && !strings.HasPrefix(fields[0], "ecdsa-") {
		return "", errors.New("that is not an SSH public key, such as the contents of ~/.ssh/id_ed25519.pub")
	}
	return fmt.Sprintf(`restrict,pty,command="/usr/bin/tmux -L wall-%s attach -r -t %s" %s %s hi-wall:%s:%s`,
		session, session, fields[0], fields[1], session, screen), nil
}

func serverWallCommand(args []string, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("wall", stderr)
	session := flags.String("session", "live", "the wall session")
	size := flags.String("size", "", "a fixed size for every screen, such as 240x67")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	usage := usageError{"usage: sudo hi server wall setup [--session S] [--size WxH]\n" +
		"       sudo hi server wall add <screen> <public key or .pub file> [--session S]\n" +
		"       sudo hi server wall remove <screen> | list"}
	if len(positional) == 0 {
		return usage
	}
	if !wallSessionName.MatchString(*session) {
		return usageError{"a session name is lowercase letters, digits, and hyphens"}
	}
	if *size != "" && !regexp.MustCompile(`^[0-9]{2,4}x[0-9]{2,4}$`).MatchString(*size) {
		return usageError{"--size is columns x rows, such as 240x67"}
	}
	if os.Geteuid() != 0 {
		return errors.New("the wall needs root to create its account and service; run it with sudo, " +
			"such as `sudo ~/.local/bin/hi server wall " + positional[0] + "`")
	}
	switch {
	case positional[0] == "setup" && len(positional) == 1:
		return wallSetup(*dirFlag, *session, *size, stdout)
	case positional[0] == "add" && len(positional) >= 3:
		return wallAdd(*session, positional[1], strings.Join(positional[2:], " "), stdout)
	case positional[0] == "remove" && len(positional) == 2:
		return wallRemove(positional[1], stdout)
	case positional[0] == "list" && len(positional) == 1:
		return wallList(stdout)
	}
	return usage
}

func runQuiet(name string, args ...string) (string, error) {
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

// wallServerDir finds the server's state as the admin who ran sudo sees it.
func wallServerDir(dirFlag string) (string, error) {
	if dirFlag != "" {
		return dirFlag, nil
	}
	if dir := os.Getenv("HI_SERVER_DIR"); dir != "" {
		return dir, nil
	}
	name := os.Getenv("SUDO_USER")
	if name == "" {
		return "", errors.New("run this with sudo from the account that runs hi server, or pass --dir")
	}
	account, err := user.Lookup(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(account.HomeDir, ".local", "state", "hi", "server"), nil
}

func wallSetup(dirFlag, session, size string, stdout io.Writer) error {
	dir, err := wallServerDir(dirFlag)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return fmt.Errorf("no hi server in %s; pass --dir", dir)
	}
	var config serverConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return err
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return errors.New("tmux is not installed; run `sudo apt install tmux`")
	}

	// 1. The account, with no sudo and no access to the server's state.
	if _, err := user.Lookup(wallUser); err != nil {
		if _, err := runQuiet("useradd", "--system", "--create-home", "--home-dir", wallHome, "--shell", "/bin/sh", wallUser); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Created the %s account.\n", wallUser)
	}
	// 2. A copy of hi it can run; rerun setup after updating hi.
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if _, err := runQuiet("install", "-D", "-m", "755", executable, wallBinary); err != nil {
		return err
	}
	// 3. Its own device key, registered as a viewer that can only watch.
	key, err := runQuiet("sudo", "-u", wallUser, "-H", wallBinary, "connect", "key")
	if err != nil {
		return err
	}
	body := map[string]string{"as": os.Getenv("SUDO_USER"), "name": "wall", "key": strings.TrimSpace(key)}
	if err := adminCall(dir, "POST", "/admin/viewers", body, nil); err != nil {
		return fmt.Errorf("register the wall as a viewer (is hi server running?): %w", err)
	}
	if _, err := runQuiet("sudo", "-u", wallUser, "-H", wallBinary, "connect", "http://"+config.Listen, "--user", "wall"); err != nil {
		return err
	}
	// 4. A service that keeps the tmux session running.
	unit := fmt.Sprintf("/etc/systemd/system/hi-wall-%s.service", session)
	if err := os.WriteFile(unit, []byte(wallUnit(session, size)), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "hi-wall-" + session}, {"restart", "hi-wall-" + session}} {
		if _, err := runQuiet("systemctl", args...); err != nil {
			return err
		}
	}
	// 5. The key file screens are added to.
	sshDir := filepath.Join(wallHome, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return err
	}
	keys := filepath.Join(sshDir, "authorized_keys")
	if _, err := os.Stat(keys); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(keys, nil, 0o600); err != nil {
			return err
		}
	}
	if _, err := runQuiet("chown", "-R", wallUser+":"+wallUser, sshDir); err != nil {
		return err
	}
	host, _ := os.Hostname()
	fmt.Fprintf(stdout, "The wall session %q is running as %s.\n", session, wallUser)
	fmt.Fprintf(stdout, "Let a screen in:  sudo %s server wall add <screen> <its public key>\n", executable)
	fmt.Fprintf(stdout, "Then on the screen: ssh -t %s@%s\n", wallUser, host)
	return nil
}

func wallKeysPath() string { return filepath.Join(wallHome, ".ssh", "authorized_keys") }

func wallAdd(session, screen, key string, stdout io.Writer) error {
	if !wallSessionName.MatchString(screen) {
		return usageError{"a screen name is lowercase letters, digits, and hyphens"}
	}
	if data, err := os.ReadFile(key); err == nil {
		key = string(data)
	}
	line, err := wallKeyLine(session, screen, key)
	if err != nil {
		return err
	}
	path := wallKeysPath()
	existing, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("run `sudo hi server wall setup` first: %w", err)
	}
	var kept []string
	for _, old := range strings.Split(strings.TrimSpace(string(existing)), "\n") {
		if old != "" && !strings.HasSuffix(old, ":"+screen) {
			kept = append(kept, old)
		}
	}
	kept = append(kept, line)
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if _, err := runQuiet("chown", wallUser+":"+wallUser, path); err != nil {
		return err
	}
	host, _ := os.Hostname()
	fmt.Fprintf(stdout, "%s can now watch the %s wall: ssh -t %s@%s\n", screen, session, wallUser, host)
	fmt.Fprintln(stdout, "Its key can only attach read-only: no shell, no other commands, no port forwarding.")
	return nil
}

func wallRemove(screen string, stdout io.Writer) error {
	path := wallKeysPath()
	existing, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var kept []string
	removed := false
	for _, line := range strings.Split(strings.TrimSpace(string(existing)), "\n") {
		if strings.HasSuffix(line, ":"+screen) {
			removed = true
			continue
		}
		if line != "" {
			kept = append(kept, line)
		}
	}
	if !removed {
		return fmt.Errorf("no screen named %q", screen)
	}
	content := strings.Join(kept, "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s can no longer watch the wall.\n", screen)
	return nil
}

func wallList(stdout io.Writer) error {
	data, err := os.ReadFile(wallKeysPath())
	if err != nil {
		return fmt.Errorf("run `sudo hi server wall setup` first: %w", err)
	}
	table := newTable(stdout)
	fmt.Fprintln(table, "SCREEN\tSESSION")
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		parts := strings.Split(fields[len(fields)-1], ":")
		if len(parts) == 3 && parts[0] == "hi-wall" {
			fmt.Fprintf(table, "%s\t%s\n", parts[2], parts[1])
			count++
		}
	}
	if count == 0 {
		fmt.Fprintln(stdout, "No screens yet; add one with `sudo hi server wall add <screen> <public key>`.")
		return nil
	}
	return table.Flush()
}
