package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
)

// hi connect joins this device to a hi server. Until it does, hi compute
// works exactly as before, with the user's own keys.

var connectPollEvery = 3 * time.Second

// serverConnection is what this device remembers about its server.
type serverConnection struct {
	URL       string        `json:"url"`
	User      string        `json:"user"`
	Group     string        `json:"group,omitempty"`
	Providers []apiProvider `json:"providers,omitempty"`
}

func hiConfigDirectory() string {
	return filepath.Dir(computeConfigPath())
}

func serverConnectionPath() string {
	return filepath.Join(hiConfigDirectory(), "server.json")
}

func deviceKeyPath() string {
	return filepath.Join(hiConfigDirectory(), "device_key")
}

// loadServerConnection returns nil when this device has never connected.
func loadServerConnection() (*serverConnection, error) {
	data, err := os.ReadFile(serverConnectionPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var connection serverConnection
	if err := json.Unmarshal(data, &connection); err != nil {
		return nil, fmt.Errorf("read %s: %w", serverConnectionPath(), err)
	}
	return &connection, nil
}

func saveServerConnection(connection serverConnection) error {
	if err := os.MkdirAll(hiConfigDirectory(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(connection, "", "  ")
	if err != nil {
		return err
	}
	path := serverConnectionPath()
	if err := os.WriteFile(path+".tmp", append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// loadDeviceKey reads the device's private key, creating it when asked. The
// private key never leaves this device.
func loadDeviceKey(create bool) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(deviceKeyPath())
	if err == nil {
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("%s is not a hi device key", deviceKeyPath())
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if !create {
		return nil, errors.New("this device has no key; run `hi connect <server>`")
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(hiConfigDirectory(), 0o700); err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(private.Seed())
	if err := os.WriteFile(deviceKeyPath(), []byte(encoded+"\n"), 0o600); err != nil {
		return nil, err
	}
	return private, nil
}

func devicePublicKey(key ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
}

// normalizeServerURL accepts compute.internal, compute.internal:7373, or a
// full http:// URL.
func normalizeServerURL(address string) (string, error) {
	address = strings.TrimRight(strings.TrimSpace(address), "/")
	if address == "" {
		return "", usageError{"usage: hi connect <server>"}
	}
	if !strings.Contains(address, "://") {
		if _, _, err := net.SplitHostPort(address); err != nil {
			address = net.JoinHostPort(address, strconv.Itoa(serverDefaultPort))
		}
		address = "http://" + address
	}
	return address, nil
}

// serverClient signs every request with the device key.
type serverClient struct {
	url  string
	key  ed25519.PrivateKey
	http *http.Client
}

func newServerClient(url string, key ed25519.PrivateKey) *serverClient {
	return &serverClient{url: url, key: key, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *serverClient) call(method, path string, body, result any) error {
	err := doJSON(c.http, method, c.url+path, body, result, c.key)
	var reply *serverReplyError
	if err != nil && !errors.As(err, &reply) {
		return fmt.Errorf("can't reach the hi server at %s: %w", c.url, err)
	}
	return err
}

func (c *serverClient) me() (apiMe, error) {
	var me apiMe
	err := c.call(http.MethodGet, "/v1/me", nil, &me)
	return me, err
}

// ---------------------------------------------------------------------------
// commands

func runConnect(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "status":
			return exitCode(connectStatusCommand(args[1:], stdout), stderr)
		case "key":
			return exitCode(connectKeyCommand(args[1:], stdout), stderr)
		case "help", "-h", "--help":
			printConnectUsage(stdout)
			return 0
		}
	}
	return exitCode(connectCommand(args, stdout, stderr), stderr)
}

func printConnectUsage(w io.Writer) {
	fmt.Fprintln(w, `hi connect joins this device to a hi server, which approves and pays for
compute. Without it, hi compute uses your own keys as before.

Usage:
  hi connect <server> [--user NAME] [--no-wait]
                        Enroll this device and wait for an admin's approval
  hi connect status     Server, user, group, and managed providers
  hi connect key        Print this device's public key, for an admin to pre-approve
  hi disconnect         Forget the server and delete this device's key`)
}

func connectCommand(args []string, stdout, stderr io.Writer) error {
	flags := &flagSet{newComputeFlags("connect", stderr)}
	user := flags.String("user", currentUserName(), "your user name on the server")
	noWait := flags.Bool("no-wait", false, "return while approval is pending")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError{"usage: hi connect <server> [--user NAME] [--no-wait]"}
	}
	url, err := normalizeServerURL(positional[0])
	if err != nil {
		return err
	}
	*user = strings.ToLower(*user)
	if !validServerName(*user) {
		return usageError{fmt.Sprintf("invalid user name %q; use lowercase letters, digits, dots, or hyphens", *user)}
	}
	if existing, err := loadServerConnection(); err != nil {
		return err
	} else if existing != nil && existing.URL != url {
		return fmt.Errorf("this device is connected to %s; run `hi disconnect` first", existing.URL)
	}
	key, err := loadDeviceKey(true)
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	client := newServerClient(url, key)
	var request serverRequest
	if err := client.call(http.MethodPost, "/v1/enroll", apiEnroll{User: *user, Hostname: hostname}, &request); err != nil {
		return err
	}
	if err := saveServerConnection(serverConnection{URL: url, User: *user}); err != nil {
		return err
	}
	if request.State == "pending" {
		fmt.Fprintf(stdout, "Asked %s to let %s join as %s (request %s).\n", url, hostname, *user, request.ID)
		fmt.Fprintf(stdout, "An admin approves it with: hi server approve %s --group <group>\n", request.ID)
		if *noWait {
			return exitStatusError{code: exitPending, message: fmt.Sprintf(
				"%s is waiting for approval; check with `hi connect status`", request.ID)}
		}
		fmt.Fprintln(stdout, "Waiting for approval… (Ctrl-C stops waiting; the request stays open)")
		for {
			time.Sleep(connectPollEvery)
			_, err := client.me()
			if err == nil {
				break
			}
			var reply *serverReplyError
			if errors.As(err, &reply) && reply.status == http.StatusForbidden {
				if reply.enroll != "" {
					continue
				}
				os.Remove(serverConnectionPath())
				return exitStatusError{code: exitDenied, message: "the server did not approve this device"}
			}
			return err
		}
	}
	return refreshConnection(client, *user, stdout)
}

// refreshConnection saves what the server manages and reports it.
func refreshConnection(client *serverClient, user string, stdout io.Writer) error {
	me, err := client.me()
	if err != nil {
		return err
	}
	if err := saveServerConnection(serverConnection{URL: client.url, User: me.User, Group: me.Group, Providers: me.Providers}); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Connected to %s as %s (%s).\n", client.url, me.User, me.Group)
	printManagedProviders(me.Providers, stdout)
	return nil
}

func printManagedProviders(providers []apiProvider, stdout io.Writer) {
	var names []string
	for _, provider := range providers {
		if provider.Name != "colab" {
			names = append(names, provider.Name)
		}
	}
	if len(names) == 0 {
		fmt.Fprintln(stdout, "The server manages no providers yet; hi compute uses your own keys.")
		return
	}
	fmt.Fprintf(stdout, "Managed by the server: %s. Starting there needs an approval.\n", strings.Join(names, ", "))
	fmt.Fprintln(stdout, "Colab and any other provider still use your own sign-in.")
}

func connectStatusCommand(args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return usageError{"usage: hi connect status"}
	}
	connection, err := loadServerConnection()
	if err != nil {
		return err
	}
	if connection == nil {
		fmt.Fprintln(stdout, "Not connected to a hi server; hi compute uses your own keys.")
		return nil
	}
	key, err := loadDeviceKey(false)
	if err != nil {
		return err
	}
	client := newServerClient(connection.URL, key)
	if _, err := client.me(); err != nil {
		var reply *serverReplyError
		if errors.As(err, &reply) && reply.enroll != "" {
			fmt.Fprintf(stdout, "Waiting for approval from %s (request %s).\n", connection.URL, reply.enroll)
			return exitStatusError{code: exitPending, message: reply.message}
		}
		return err
	}
	return refreshConnection(client, connection.User, stdout)
}

func connectKeyCommand(args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return usageError{"usage: hi connect key"}
	}
	key, err := loadDeviceKey(true)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, devicePublicKey(key))
	return nil
}

func runDisconnect(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: hi disconnect")
		return 2
	}
	connection, err := loadServerConnection()
	if err != nil {
		return exitCode(err, stderr)
	}
	if connection == nil {
		fmt.Fprintln(stdout, "Not connected to a hi server.")
		return 0
	}
	for _, path := range []string{serverConnectionPath(), deviceKeyPath()} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return exitCode(err, stderr)
		}
	}
	fmt.Fprintf(stdout, "Disconnected from %s. hi compute uses your own keys again.\n", connection.URL)
	fmt.Fprintln(stdout, "Instances you started through the server keep its limits until they stop.")
	return 0
}

// ---------------------------------------------------------------------------
// small shared helpers

// flagSet parses flags before and after positional arguments.
type flagSet struct{ *flag.FlagSet }

func (f *flagSet) parse(args []string) ([]string, error) {
	return parseInterspersedFlags(f.FlagSet, args)
}

func newTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
}

func readPassword(file *os.File) ([]byte, error) {
	return term.ReadPassword(int(file.Fd()))
}

// Exit statuses for requests that wait on a person: pending and denied.
const (
	exitPending = 3
	exitDenied  = 4
)

type exitStatusError struct {
	code    int
	message string
}

func (e exitStatusError) Error() string { return e.message }
