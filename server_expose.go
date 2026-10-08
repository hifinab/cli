package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

// hi server expose: a second listener with only what cloud machines may
// use (the hi data proxy), published with `netbird expose` while runs need
// it. The client API stays inside NetBird. Cloud machines authenticate with
// run tokens: hi data tokens marked for cloud use, for named repositories,
// for the run's lifetime (docs/specs/approved/hi_server_expose.md).

const (
	exposeDefaultPort   = 7374
	exposeIdleAfter     = 10 * time.Minute
	exposeStartTimeout  = 45 * time.Second
	runTokenGrace       = 15 * time.Minute
	runTokenMaxLifetime = 7 * 24 * time.Hour
)

// exposeCommand is NetBird's CLI; tests replace it.
var exposeCommand = "netbird"

var exposeURLLine = regexp.MustCompile(`URL:\s*(https?://\S+)`)

// serverExposure is the running `netbird expose`, if any.
type serverExposure struct {
	mu      sync.Mutex
	listen  string // the instance listener's address; "" when off
	cmd     *exec.Cmd
	done    chan struct{} // closed once cmd has exited and it's audited
	url     string
	started time.Time
	// until is when the last run token issued expires; lastUse is the
	// last call through the listener.
	until   time.Time
	lastUse time.Time
	problem string
}

// exposeListenAddress is the instance listener's address: the configured
// one, or the client listener's host on port 7374. NetBird's proxy reaches
// it over WireGuard, so it must be on the NetBird address, not localhost.
func exposeListenAddress(config serverConfig) string {
	if config.ExposeListen == "off" {
		return ""
	}
	if config.ExposeListen != "" {
		return config.ExposeListen
	}
	host, _, err := net.SplitHostPort(config.Listen)
	if err != nil || host == "" {
		return ""
	}
	return net.JoinHostPort(host, fmt.Sprint(exposeDefaultPort))
}

// instanceHandler serves cloud machines: the data proxy for run tokens,
// and a health check. Everything else is a 404.
func (s *hiServer) instanceHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle(dataProxyPath+"/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.exposure.mu.Lock()
		s.exposure.lastUse = time.Now()
		public := s.exposure.url
		s.exposure.mu.Unlock()
		s.serveDataProxy(w, r, true, public)
	}))
	return mux
}

// ensureExposure starts `netbird expose` if it isn't running and returns
// the public URL once it answers.
func (s *hiServer) ensureExposure(name string) (string, error) {
	e := &s.exposure
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.listen == "" {
		return "", errors.New("this hi server doesn't expose anything to cloud machines (expose_listen is off)")
	}
	if e.cmd != nil && e.url != "" {
		return e.url, nil
	}
	_, port, err := net.SplitHostPort(e.listen)
	if err != nil {
		return "", err
	}
	cmd, url, _, err := startNetbirdExpose([]string{"expose", port, "--with-name-prefix", "hi-" + name}, exposeStartTimeout)
	if err != nil {
		e.problem = err.Error()
		return "", err
	}
	stopCmd := func() {
		cmd.Process.Signal(os.Interrupt)
		cmd.Wait()
	}
	// The name can take a few seconds to answer.
	deadline := time.Now().Add(exposeStartTimeout)
	for {
		response, err := hubClient.Get(url + "/health")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			stopCmd()
			return "", fmt.Errorf("%s doesn't answer; is the instance listener on the NetBird address?", url)
		}
		time.Sleep(time.Second)
	}
	done := make(chan struct{})
	e.cmd, e.done, e.url, e.started, e.problem = cmd, done, url, time.Now(), ""
	go func() {
		defer close(done)
		cmd.Wait()
		e.mu.Lock()
		if e.cmd == cmd {
			e.cmd, e.url = nil, ""
		}
		e.mu.Unlock()
		s.audit("server", "exposure ended", url, "")
	}()
	s.audit("server", "exposure started", url, "listener "+e.listen)
	return url, nil
}

// reapExposure stops the exposure once no run token is valid and nothing
// has called for a while.
func (s *hiServer) reapExposure() {
	s.pruneRuns()
	e := &s.exposure
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cmd == nil || time.Now().Before(e.until) || time.Since(e.lastUse) < exposeIdleAfter {
		return
	}
	e.cmd.Process.Signal(os.Interrupt)
}

// stopExposure stops `netbird expose` and waits until it has exited.
func (s *hiServer) stopExposure() {
	e := &s.exposure
	e.mu.Lock()
	cmd, done := e.cmd, e.done
	e.mu.Unlock()
	if cmd == nil {
		return
	}
	cmd.Process.Signal(os.Interrupt)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

// ---------------------------------------------------------------------------
// run tokens

// apiRunAccess is POST /v1/data/run-access's answer.
type apiRunAccess struct {
	Endpoint string    `json:"endpoint"` // HF_ENDPOINT for the machine
	Token    string    `json:"token"`
	Expires  time.Time `json:"expires"`
}

func (s *hiServer) handleRunAccess(w http.ResponseWriter, _ *http.Request, device serverDevice, body []byte) {
	group, ok := s.userGroup(device.User)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "a dashboard viewer can't use hi data")
		return
	}
	var input struct {
		Scopes  []string `json:"scopes"`
		Seconds int64    `json:"seconds"`
		Run     string   `json:"run"`
	}
	if err := json.Unmarshal(body, &input); err != nil || len(input.Scopes) == 0 {
		writeAPIError(w, http.StatusBadRequest, "name the repositories the run may read")
		return
	}
	policy, orgs := s.policy(), s.dataOrgs()
	for _, scope := range input.Scopes {
		_, id, err := parseDataScope(scope)
		if err == nil && id == "" {
			err = errors.New("a run token names its repositories; * is not allowed")
		}
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		org, _, _ := strings.Cut(id, "/")
		if _, ok := orgs[org]; !ok {
			writeAPIError(w, http.StatusNotFound, fmt.Sprintf("this server doesn't serve the %s organization", org))
			return
		}
		if !dataAllowed(policy, group, id) {
			writeAPIError(w, http.StatusForbidden, fmt.Sprintf("your group %s may not read %s", group, id))
			return
		}
	}
	run := strings.TrimSpace(input.Run)
	if run == "" {
		run = newServerID("run")
	}
	s.mu.Lock()
	revoked := s.state.Runs[run] != nil && !s.state.Runs[run].Revoked.IsZero()
	s.mu.Unlock()
	if revoked {
		writeAPIError(w, http.StatusForbidden, fmt.Sprintf("the run %s was revoked on this server; start it under another name", run))
		return
	}
	lifetime := 24 * time.Hour
	if input.Seconds > 0 {
		lifetime = time.Duration(input.Seconds) * time.Second
	}
	lifetime = min(lifetime+runTokenGrace, runTokenMaxLifetime)
	url, err := s.ensureExposure(s.exposeName())
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	expires := computeNow().Add(lifetime)
	token := s.issueDataToken(dataClaims{Device: device.Fingerprint, User: device.User, Scopes: input.Scopes,
		Cloud: true, Run: run, Expires: expires.Unix()})
	s.mu.Lock()
	if s.state.Runs == nil {
		s.state.Runs = map[string]*serverRun{}
	}
	entry := s.state.Runs[run]
	if entry == nil {
		entry = &serverRun{Run: run, Issued: computeNow().UTC()}
		s.state.Runs[run] = entry
	}
	entry.User, entry.Device, entry.Hostname, entry.Scopes = device.User, device.Fingerprint, device.Hostname, input.Scopes
	if expires.After(entry.Expires) {
		entry.Expires = expires.UTC()
	}
	s.saveLocked()
	s.mu.Unlock()
	s.exposure.mu.Lock()
	if expires.After(s.exposure.until) {
		s.exposure.until = expires
	}
	s.exposure.mu.Unlock()
	s.audit(device.User, "run token", strings.Join(input.Scopes, ", "),
		fmt.Sprintf("run %s from %s, until %s, through %s", run, device.Hostname, expires.Format(time.RFC3339), url))
	writeJSON(w, http.StatusOK, apiRunAccess{Endpoint: url + dataProxyPath, Token: token, Expires: expires})
}

// serverRun is a cloud run that was given a run token.
type serverRun struct {
	Run      string    `json:"run"`
	User     string    `json:"user"`
	Device   string    `json:"device"`
	Hostname string    `json:"hostname,omitempty"`
	Scopes   []string  `json:"scopes"`
	Issued   time.Time `json:"issued"`
	Expires  time.Time `json:"expires"`
	Revoked  time.Time `json:"revoked,omitzero"`
}

// runRevoked reports whether a run's tokens were revoked.
func (s *hiServer) runRevoked(run string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.state.Runs[run]
	return entry != nil && !entry.Revoked.IsZero()
}

// revokeRun ends a run's tokens now. The server keeps the run until its
// tokens would have expired.
func (s *hiServer) revokeRun(run, actor string) (*serverRun, error) {
	s.mu.Lock()
	entry := s.state.Runs[run]
	if entry == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("no run %s has a valid run token; see hi server expose", run)
	}
	already := !entry.Revoked.IsZero()
	if !already {
		entry.Revoked = computeNow().UTC()
	}
	copied := *entry
	s.saveLocked()
	// The exposure stays up only for runs that weren't revoked.
	var until time.Time
	for _, other := range s.state.Runs {
		if other.Revoked.IsZero() && other.Expires.After(until) {
			until = other.Expires
		}
	}
	s.mu.Unlock()
	s.exposure.mu.Lock()
	s.exposure.until = until
	s.exposure.mu.Unlock()
	if !already {
		s.audit(actor, "run token revoked", run, fmt.Sprintf("%s from %s, for %s", copied.User, copied.Hostname, strings.Join(copied.Scopes, ", ")))
	}
	return &copied, nil
}

// pruneRuns forgets runs whose tokens have expired.
func (s *hiServer) pruneRuns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for run, entry := range s.state.Runs {
		if computeNow().After(entry.Expires) {
			delete(s.state.Runs, run)
			changed = true
		}
	}
	if changed {
		s.saveLocked()
	}
}

func (s *hiServer) runList() []serverRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	var runs []serverRun
	for _, entry := range s.state.Runs {
		if computeNow().Before(entry.Expires) {
			runs = append(runs, *entry)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Issued.Before(runs[j].Issued) })
	return runs
}

// exposeName is the prefix for NetBird's generated service name.
func (s *hiServer) exposeName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "server"
	}
	name := strings.ToLower(host)
	name = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(name, "-")
	return strings.Trim(name, "-")
}

// ---------------------------------------------------------------------------
// admin

type apiExposure struct {
	Listen  string    `json:"listen"`
	URL     string    `json:"url,omitempty"`
	Started time.Time `json:"started,omitzero"`
	Until   time.Time `json:"until,omitzero"`
	LastUse time.Time   `json:"last_use,omitzero"`
	Problem string      `json:"problem,omitempty"`
	Runs    []serverRun `json:"runs,omitempty"`
}

func (s *hiServer) exposeAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/expose", func(w http.ResponseWriter, _ *http.Request) {
		runs := s.runList()
		e := &s.exposure
		e.mu.Lock()
		defer e.mu.Unlock()
		writeJSON(w, http.StatusOK, apiExposure{Listen: e.listen, URL: e.url, Started: e.started, Until: e.until, LastUse: e.lastUse, Problem: e.problem, Runs: runs})
	})
	mux.HandleFunc("POST /admin/expose/revoke", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Run string `json:"run"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Run == "" {
			writeAPIError(w, http.StatusBadRequest, "name the run to revoke")
			return
		}
		run, err := s.revokeRun(input.Run, "admin")
		if err != nil {
			writeAPIError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, run)
	})
	mux.HandleFunc("POST /admin/expose/stop", func(w http.ResponseWriter, _ *http.Request) {
		s.stopExposure()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
}

func serverExposeCommand(args []string, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("expose", stderr)
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	switch {
	case len(positional) == 0:
		var status apiExposure
		if err := adminCall(*dirFlag, http.MethodGet, "/admin/expose", nil, &status); err != nil {
			return err
		}
		switch {
		case status.Listen == "":
			fmt.Fprintln(stdout, "Exposure is off (expose_listen is \"off\" in config.json).")
		case status.URL == "":
			fmt.Fprintf(stdout, "Not exposed now. Cloud runs that need it open %s with netbird expose.\n", status.Listen)
		default:
			fmt.Fprintf(stdout, "Exposed at %s since %s, for %s.\n", status.URL, status.Started.Local().Format("15:04"), status.Listen)
			if !status.Until.IsZero() {
				fmt.Fprintf(stdout, "Run tokens are valid until %s.\n", status.Until.Local().Format("2006-01-02 15:04"))
			}
		}
		if status.Problem != "" {
			fmt.Fprintf(stdout, "Last problem: %s\n", status.Problem)
		}
		if len(status.Runs) > 0 {
			fmt.Fprintln(stdout, "\nRuns with run tokens:")
			table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(table, "  RUN\tUSER\tFROM\tDATA\tUNTIL")
			for _, run := range status.Runs {
				until := run.Expires.Local().Format("2006-01-02 15:04")
				if !run.Revoked.IsZero() {
					until = "revoked " + run.Revoked.Local().Format("2006-01-02 15:04")
				}
				fmt.Fprintf(table, "  %s\t%s\t%s\t%s\t%s\n", run.Run, run.User, firstNonEmpty(run.Hostname, "-"), strings.Join(run.Scopes, ", "), until)
			}
			table.Flush()
			fmt.Fprintln(stdout, "hi server expose revoke <run> ends one run's token now.")
		}
		return nil
	case len(positional) == 1 && positional[0] == "stop":
		if err := adminCall(*dirFlag, http.MethodPost, "/admin/expose/stop", nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Stopped the exposure. Runs using it fail until they ask again.")
		return nil
	case len(positional) == 2 && positional[0] == "revoke":
		var run serverRun
		if err := adminCall(*dirFlag, http.MethodPost, "/admin/expose/revoke", map[string]string{"run": positional[1]}, &run); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Revoked the run token of %s (%s, for %s). The run can't read the team's data any more;\nthe server remembers this until %s.\n",
			run.Run, run.User, strings.Join(run.Scopes, ", "), run.Expires.Local().Format("2006-01-02 15:04"))
		return nil
	}
	return usageError{"usage: hi server expose [stop | revoke <run>]"}
}
