package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// hi net expose puts a service on this machine on a temporary public HTTPS
// address with `netbird expose`, adding what that lacks: a relay so
// services on localhost work, a time limit, protection by default, a
// request log, and background use for agents
// (docs/specs/ideas/hi_net_expose.md).

const (
	netExposeDefaultMax = time.Hour
	netExposeMaxMax     = 24 * time.Hour
	netExposeReadyAfter = 60 * time.Second
)

// netExposeProbeHeader marks hi's own readiness check, which the request
// log leaves out.
const netExposeProbeHeader = "X-Hi-Expose-Probe"

// netExposeAddress finds the NetBird address; tests replace it.
var netExposeAddress = netbirdAddress

// netExposeOptions is everything one exposure needs; --detach passes it to
// the background process as JSON.
type netExposeOptions struct {
	Port       int           `json:"port"`
	Host       string        `json:"host"`
	Max        time.Duration `json:"max"`
	Protection string        `json:"protection"` // password, pin, groups, public
	Secret     string        `json:"secret,omitempty"`
	Groups     string        `json:"groups,omitempty"`
	Name       string        `json:"name"`
	JSON       bool          `json:"json,omitempty"`
}

// netExposeState is a running exposure, in the state folder.
type netExposeState struct {
	Name       string    `json:"name"`
	URL        string    `json:"url"`
	Port       int       `json:"port"`
	Host       string    `json:"host"`
	Protection string    `json:"protection"`
	Password   string    `json:"password,omitempty"`
	PIN        string    `json:"pin,omitempty"`
	Groups     string    `json:"groups,omitempty"`
	PID        int       `json:"pid"`
	Started    time.Time `json:"started"`
	Expires    time.Time `json:"expires"`
}

func netExposeDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "hi", "net", "expose")
}

func printNetExposeUsage(w io.Writer) {
	fmt.Fprintln(w, `hi net expose puts a service on this machine on a temporary public HTTPS
address through NetBird, protected by a password unless you choose otherwise.

usage:
  hi net expose <port> [options]     expose localhost:<port> until --max or Ctrl+C
  hi net expose ls [--json]          what this machine is exposing
  hi net expose stop <name> | --all  stop it now

options:
  --max <duration>     how long (default 1h, at most 24h)
  --password <text>    a password page in front (default: one is generated)
  --pin <6 digits>     a PIN page instead
  --groups <g1,g2>     NetBird SSO for users in these groups instead
  --public             no protection: anyone with the address (webhooks, API clients)
  --name <prefix>      the start of the address (default hi-<host>-<port>)
  --host <addr>        where the service listens (default localhost)
  --detach             run in the background; print the address and return
  --json               print the result as JSON

Password, PIN, and SSO are login pages for browsers; scripts and webhook
senders need --public. Needs Peer Expose on in NetBird (Settings > Clients).`)
}

func runNetExpose(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printNetExposeUsage(stdout)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "ls", "list":
		return exitCode(netExposeList(args[1:], stdout), stderr)
	case "stop":
		return exitCode(netExposeStop(args[1:], stdout), stderr)
	case "__run":
		// The background half of --detach.
		var options netExposeOptions
		if len(args) != 2 || json.Unmarshal([]byte(args[1]), &options) != nil {
			return 2
		}
		return exitCode(netExposeRun(options, true, stdout, stderr), stderr)
	}
	options, detach, err := parseNetExpose(args)
	if err != nil {
		fmt.Fprintf(stderr, "hi: %v\n\n", err)
		printNetExposeUsage(stderr)
		return 2
	}
	if detach {
		return exitCode(netExposeDetach(options, stdout, stderr), stderr)
	}
	return exitCode(netExposeRun(options, false, stdout, stderr), stderr)
}

var netExposePrefix = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

func parseNetExpose(args []string) (netExposeOptions, bool, error) {
	flags := &flagSet{newComputeFlags("net expose", io.Discard)}
	maxLifetime := &lifetimeFlag{value: netExposeDefaultMax}
	flags.Var(maxLifetime, "max", "how long")
	password := flags.String("password", "", "password")
	pin := flags.String("pin", "", "PIN")
	groups := flags.String("groups", "", "SSO groups")
	public := flags.Bool("public", false, "no protection")
	name := flags.String("name", "", "address prefix")
	host := flags.String("host", "localhost", "where the service listens")
	detach := flags.Bool("detach", false, "run in the background")
	asJSON := flags.Bool("json", false, "JSON output")
	positional, err := flags.parse(args)
	if err != nil {
		return netExposeOptions{}, false, err
	}
	if len(positional) != 1 {
		return netExposeOptions{}, false, errors.New("name one port, as in hi net expose 3000")
	}
	port, err := parsePort(positional[0])
	if err != nil {
		return netExposeOptions{}, false, err
	}
	options := netExposeOptions{Port: port, Host: *host, Max: maxLifetime.value, JSON: *asJSON}
	switch {
	case options.Max == noLimit || options.Max > netExposeMaxMax:
		return options, false, fmt.Errorf("--max is at most %s; an exposure always ends", netExposeMaxMax)
	case options.Max <= 0:
		options.Max = netExposeDefaultMax
	}
	chosen := 0
	for _, set := range []bool{*password != "", *pin != "", *groups != "", *public} {
		if set {
			chosen++
		}
	}
	if chosen > 1 {
		return options, false, errors.New("choose one of --password, --pin, --groups, and --public")
	}
	switch {
	case *public:
		options.Protection = "public"
	case *pin != "":
		if !regexp.MustCompile(`^[0-9]{6}$`).MatchString(*pin) {
			return options, false, errors.New("--pin is 6 digits")
		}
		options.Protection, options.Secret = "pin", *pin
	case *groups != "":
		options.Protection, options.Groups = "groups", *groups
	default:
		options.Protection, options.Secret = "password", *password
		if options.Secret == "" {
			options.Secret = netExposePassword()
		}
	}
	options.Name = *name
	if options.Name == "" {
		hostname, _ := os.Hostname()
		options.Name = fmt.Sprintf("hi-%s-%d", strings.ToLower(regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(hostname, "-")), port)
		options.Name = strings.Trim(options.Name, "-")
		if len(options.Name) > 31 {
			options.Name = options.Name[:31]
		}
	}
	if !netExposePrefix.MatchString(options.Name) {
		return options, false, fmt.Errorf("--name %q: lowercase letters, digits, and dashes, up to 31", options.Name)
	}
	return options, *detach, nil
}

var netExposeWords = []string{"amber", "birch", "cedar", "delta", "ember", "fjord", "grove", "heron",
	"iris", "juniper", "kestrel", "lark", "maple", "nectar", "otter", "plum", "quartz", "raven",
	"sage", "tundra", "umber", "violet", "willow", "yarrow", "zephyr", "comet", "harbor", "meadow"}

// netExposePassword is three words and two digits, easy to pass on.
func netExposePassword() string {
	pick := func(n int) int {
		value, _ := rand.Int(rand.Reader, big.NewInt(int64(n)))
		return int(value.Int64())
	}
	words := make([]string, 3)
	for i := range words {
		words[i] = netExposeWords[pick(len(netExposeWords))]
	}
	return fmt.Sprintf("%s-%02d", strings.Join(words, "-"), pick(100))
}

// netExposeArgs is the netbird expose command line for the relay's port.
func netExposeArgs(options netExposeOptions, relayPort int) []string {
	args := []string{"expose", fmt.Sprint(relayPort), "--with-name-prefix", options.Name}
	switch options.Protection {
	case "password":
		args = append(args, "--with-password", options.Secret)
	case "pin":
		args = append(args, "--with-pin", options.Secret)
	case "groups":
		args = append(args, "--with-user-groups", options.Groups)
	}
	return args
}

// ---------------------------------------------------------------------------
// netbird expose, shared with hi server expose

var exposeNameLine = regexp.MustCompile(`Name:\s*(\S+)`)

// startNetbirdExpose runs `netbird expose` and returns once it prints the
// URL. The process keeps the service until it is stopped.
func startNetbirdExpose(args []string, timeout time.Duration) (cmd *exec.Cmd, url, name string, err error) {
	cmd = exec.Command(exposeCommand, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", "", err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, "", "", fmt.Errorf("netbird expose: %w; is NetBird installed (hi install netbird)?", err)
	}
	type found struct{ url, name string }
	result := make(chan found, 1)
	var mu sync.Mutex
	var output strings.Builder
	go func() {
		buffer := make([]byte, 4096)
		var seen found
		var text strings.Builder
		sent := false
		for {
			n, readErr := stdout.Read(buffer)
			if n > 0 && !sent {
				text.Write(buffer[:n])
				mu.Lock()
				output.Write(buffer[:n])
				mu.Unlock()
				if match := exposeNameLine.FindStringSubmatch(text.String()); match != nil {
					seen.name = match[1]
				}
				if match := exposeURLLine.FindStringSubmatch(text.String()); match != nil {
					seen.url = match[1]
					result <- seen
					sent = true
				}
			}
			if readErr != nil {
				if !sent {
					close(result)
				}
				return
			}
		}
	}()
	select {
	case got, ok := <-result:
		if !ok {
			cmd.Wait()
			mu.Lock()
			text := strings.TrimSpace(output.String())
			mu.Unlock()
			if strings.Contains(text, "not enabled") {
				text += "; a NetBird admin turns on Peer Expose in Settings > Clients"
			}
			return nil, "", "", fmt.Errorf("netbird expose: %s", text)
		}
		return cmd, got.url, got.name, nil
	case <-time.After(timeout):
		cmd.Process.Kill()
		cmd.Wait()
		return nil, "", "", errors.New("netbird expose gave no address in time")
	}
}

// ---------------------------------------------------------------------------
// the relay

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach the hijacker for WebSockets.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// newNetExposeRelay passes requests to the local service, keeping the
// Host header the browser sent, and logs one line per request.
func newNetExposeRelay(target string, log io.Writer) http.Handler {
	upstream, _ := url.Parse(target)
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		proxy := &httputil.ReverseProxy{
			Rewrite: func(out *httputil.ProxyRequest) {
				out.SetURL(upstream)
				out.Out.Host = out.In.Host
				out.SetXForwarded()
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				http.Error(w, "hi net expose: nothing answers on "+upstream.Host+" on the exposing machine", http.StatusBadGateway)
			},
			FlushInterval: -1,
		}
		proxy.ServeHTTP(recorder, r)
		if r.Header.Get(netExposeProbeHeader) != "" {
			return // hi's own check that the address works
		}
		mu.Lock()
		fmt.Fprintf(log, "%-6s %-40s %d  %d ms\n", r.Method, clipPath(r.URL.Path), recorder.status, time.Since(started).Milliseconds())
		mu.Unlock()
	})
}

func clipPath(path string) string {
	if len(path) > 40 {
		return path[:37] + "..."
	}
	return path
}

// ---------------------------------------------------------------------------
// running an exposure

func netExposeRun(options netExposeOptions, background bool, stdout, stderr io.Writer) error {
	address, err := netExposeAddress()
	if err != nil {
		return errors.New("NetBird isn't connected on this machine; connect with `hi net` first")
	}
	target := "http://" + net.JoinHostPort(options.Host, fmt.Sprint(options.Port))
	if conn, err := net.DialTimeout("tcp", net.JoinHostPort(options.Host, fmt.Sprint(options.Port)), 2*time.Second); err != nil {
		fmt.Fprintf(stderr, "hi: warning: nothing answers on %s:%d yet; visitors get an error page until it does\n", options.Host, options.Port)
	} else {
		conn.Close()
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(address, "0"))
	if err != nil {
		return fmt.Errorf("can't open the relay on the NetBird address: %w", err)
	}
	relay := &http.Server{Handler: newNetExposeRelay(target, stdout), ReadHeaderTimeout: 30 * time.Second}
	go relay.Serve(listener)
	defer relay.Close()
	relayPort := listener.Addr().(*net.TCPAddr).Port

	cmd, publicURL, name, err := startNetbirdExpose(netExposeArgs(options, relayPort), netExposeReadyAfter)
	if err != nil {
		return err
	}
	stop := func() {
		cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
		}
	}
	defer stop()
	if name == "" {
		name = options.Name
	}
	if err := waitForNetExpose(publicURL, netExposeReadyAfter); err != nil {
		return err
	}
	started := time.Now()
	state := netExposeState{Name: name, URL: publicURL, Port: options.Port, Host: options.Host, Protection: options.Protection,
		Groups: options.Groups, PID: os.Getpid(), Started: started, Expires: started.Add(options.Max)}
	switch options.Protection {
	case "password":
		state.Password = options.Secret
	case "pin":
		state.PIN = options.Secret
	}
	if err := os.MkdirAll(netExposeDir(), 0o700); err != nil {
		return err
	}
	statePath := filepath.Join(netExposeDir(), name+".json")
	data, _ := json.MarshalIndent(state, "", "  ")
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		return err
	}
	defer os.Remove(statePath)
	reportNetExpose(state, true)
	defer reportNetExpose(state, false)

	if !background {
		printNetExposeState(state, options.JSON, stdout)
		if !options.JSON {
			fmt.Fprintf(stdout, "  until %s; Ctrl+C stops it sooner\n", state.Expires.Local().Format("15:04"))
		}
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	timer := time.NewTimer(options.Max)
	defer timer.Stop()
	select {
	case <-signals:
		fmt.Fprintf(stderr, "Stopped exposing %s.\n", publicURL)
	case <-timer.C:
		fmt.Fprintf(stderr, "Stopped exposing %s: its --max of %s is up.\n", publicURL, options.Max)
	case err := <-exited:
		return fmt.Errorf("netbird expose stopped on its own (%v); the address is gone", err)
	}
	return nil
}

// waitForNetExpose waits until the address reaches the relay: anything but
// NetBird's own "can't reach the service" page.
func waitForNetExpose(address string, timeout time.Duration) error {
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	deadline := time.Now().Add(timeout)
	for {
		request, _ := http.NewRequest(http.MethodGet, address, nil)
		request.Header.Set(netExposeProbeHeader, "1")
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode != http.StatusBadGateway && response.StatusCode != http.StatusNotFound {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s doesn't answer yet; NetBird may be slow, so try again", address)
		}
		time.Sleep(time.Second)
	}
}

func printNetExposeState(state netExposeState, asJSON bool, stdout io.Writer) {
	if asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		encoder.Encode(state)
		return
	}
	fmt.Fprintf(stdout, "Exposing %s:%d at %s\n", state.Host, state.Port, state.URL)
	switch state.Protection {
	case "password":
		fmt.Fprintf(stdout, "  password: %s (a login page asks for it)\n", state.Password)
	case "pin":
		fmt.Fprintf(stdout, "  PIN: %s (a login page asks for it)\n", state.PIN)
	case "groups":
		fmt.Fprintf(stdout, "  for NetBird users in %s, through SSO\n", state.Groups)
	case "public":
		fmt.Fprintln(stdout, "  public: anyone with the address can use it")
	}
}

// reportNetExpose tells a connected hi server, for its audit log and live
// feed. It never fails the exposure.
func reportNetExpose(state netExposeState, open bool) {
	connection, err := loadServerConnection()
	if err != nil || connection == nil {
		return
	}
	key, err := loadDeviceKey(false)
	if err != nil {
		return
	}
	client := newServerClient(connection.URL, key)
	client.http.Timeout = 5 * time.Second
	detail := fmt.Sprintf("port %d, %s, until %s", state.Port, state.Protection, state.Expires.UTC().Format(time.RFC3339))
	client.call(http.MethodPost, "/v1/activity", apiActivity{Instance: state.URL, Command: "expose", Open: open, Detail: detail}, nil)
}

// ---------------------------------------------------------------------------
// --detach, ls, and stop

func netExposeDetach(options netExposeOptions, stdout, stderr io.Writer) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(netExposeDir(), 0o700); err != nil {
		return err
	}
	before := netExposeStates()
	encoded, _ := json.Marshal(options)
	logPath := filepath.Join(netExposeDir(), fmt.Sprintf("%s-%d.log", options.Name, time.Now().Unix()))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(executable, "net", "expose", "__run", string(encoded))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	deadline := time.After(2*netExposeReadyAfter + 10*time.Second)
	for {
		for _, state := range netExposeStates() {
			if state.PID == cmd.Process.Pid && before[state.Name].PID != state.PID {
				cmd.Process.Release()
				printNetExposeState(state, options.JSON, stdout)
				if !options.JSON {
					fmt.Fprintf(stdout, "  until %s, in the background; stop it with: hi net expose stop %s\n  requests are logged in %s\n",
						state.Expires.Local().Format("15:04"), state.Name, logPath)
				}
				return nil
			}
		}
		select {
		case <-exited:
			data, _ := os.ReadFile(logPath)
			return fmt.Errorf("the exposure didn't start: %s", strings.TrimSpace(strings.TrimPrefix(string(data), "hi: ")))
		case <-deadline:
			cmd.Process.Signal(syscall.SIGTERM)
			return errors.New("the exposure didn't start in time; see " + logPath)
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// netExposeStates reads the running exposures, dropping those whose
// process is gone.
func netExposeStates() map[string]netExposeState {
	states := map[string]netExposeState{}
	entries, _ := os.ReadDir(netExposeDir())
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(netExposeDir(), entry.Name())
		data, err := os.ReadFile(path)
		var state netExposeState
		if err != nil || json.Unmarshal(data, &state) != nil {
			continue
		}
		if process, err := os.FindProcess(state.PID); err != nil || process.Signal(syscall.Signal(0)) != nil {
			os.Remove(path)
			continue
		}
		states[state.Name] = state
	}
	return states
}

func netExposeList(args []string, stdout io.Writer) error {
	asJSON := len(args) == 1 && args[0] == "--json"
	if len(args) > 0 && !asJSON {
		return usageError{"usage: hi net expose ls [--json]"}
	}
	states := netExposeStates()
	names := make([]string, 0, len(states))
	for name := range states {
		names = append(names, name)
	}
	sort.Strings(names)
	if asJSON {
		list := []netExposeState{}
		for _, name := range names {
			list = append(list, states[name])
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(list)
	}
	if len(names) == 0 {
		fmt.Fprintln(stdout, "Nothing is exposed from this machine.")
		return nil
	}
	table := newTable(stdout)
	fmt.Fprintln(table, "NAME\tURL\tPORT\tPROTECTION\tUNTIL")
	for _, name := range names {
		state := states[name]
		protection := state.Protection
		switch {
		case state.Password != "":
			protection += " " + state.Password
		case state.PIN != "":
			protection += " " + state.PIN
		case state.Groups != "":
			protection += " " + state.Groups
		}
		fmt.Fprintf(table, "%s\t%s\t%d\t%s\t%s\n", name, state.URL, state.Port, protection, state.Expires.Local().Format("15:04"))
	}
	return table.Flush()
}

func netExposeStop(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return usageError{"usage: hi net expose stop <name> | --all"}
	}
	states := netExposeStates()
	var targets []netExposeState
	for name, state := range states {
		if args[0] == "--all" || name == args[0] {
			targets = append(targets, state)
		}
	}
	if len(targets) == 0 {
		if args[0] == "--all" {
			fmt.Fprintln(stdout, "Nothing is exposed from this machine.")
			return nil
		}
		return fmt.Errorf("nothing named %q is exposed; hi net expose ls shows what is", args[0])
	}
	for _, state := range targets {
		if process, err := os.FindProcess(state.PID); err == nil {
			process.Signal(syscall.SIGTERM)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		for ctx.Err() == nil {
			if _, err := os.Stat(filepath.Join(netExposeDir(), state.Name+".json")); errors.Is(err, os.ErrNotExist) {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		cancel()
		fmt.Fprintf(stdout, "Stopped %s (%s).\n", state.Name, state.URL)
	}
	return nil
}
