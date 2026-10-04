package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// hi server expose: a second listener with only what cloud machines may
// use (the hi data proxy), published with `netbird expose` while runs need
// it. The client API stays inside NetBird. Cloud machines authenticate with
// run tokens: hi data tokens marked for cloud use, for named repositories,
// for the run's lifetime (docs/specs/ideas/hi_server_expose.md).

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
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, exposeCommand, "expose", port, "--with-name-prefix", "hi-"+name)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return "", err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		cancel()
		return "", fmt.Errorf("netbird expose: %w", err)
	}
	found := make(chan string, 1)
	var output strings.Builder
	go func() {
		scanner := bufio.NewScanner(stdout)
		sent := false
		for scanner.Scan() {
			line := scanner.Text()
			if !sent {
				output.WriteString(line + "\n")
				if match := exposeURLLine.FindStringSubmatch(line); match != nil {
					found <- match[1]
					sent = true
				}
			}
		}
		if !sent {
			close(found)
		}
	}()
	var url string
	select {
	case got, ok := <-found:
		if !ok {
			cmd.Wait()
			cancel()
			text := strings.TrimSpace(output.String())
			if strings.Contains(text, "not enabled") {
				text += "; a NetBird admin turns on Peer Expose in Settings > Clients"
			}
			e.problem = text
			return "", fmt.Errorf("netbird expose: %s", text)
		}
		url = got
	case <-time.After(exposeStartTimeout):
		cancel()
		cmd.Wait()
		return "", errors.New("netbird expose gave no URL in time")
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
			cancel()
			cmd.Wait()
			return "", fmt.Errorf("%s doesn't answer; is the instance listener on the NetBird address?", url)
		}
		time.Sleep(time.Second)
	}
	e.cmd, e.url, e.started, e.problem = cmd, url, time.Now(), ""
	go func() {
		cmd.Wait()
		cancel()
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
	e := &s.exposure
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cmd == nil || time.Now().Before(e.until) || time.Since(e.lastUse) < exposeIdleAfter {
		return
	}
	e.cmd.Process.Signal(os.Interrupt)
}

func (s *hiServer) stopExposure() {
	e := &s.exposure
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cmd != nil {
		e.cmd.Process.Signal(os.Interrupt)
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
		Cloud: true, Run: input.Run, Expires: expires.Unix()})
	s.exposure.mu.Lock()
	if expires.After(s.exposure.until) {
		s.exposure.until = expires
	}
	s.exposure.mu.Unlock()
	s.audit(device.User, "run token", strings.Join(input.Scopes, ", "),
		fmt.Sprintf("run %s from %s, until %s, through %s", firstNonEmpty(input.Run, "-"), device.Hostname, expires.Format(time.RFC3339), url))
	writeJSON(w, http.StatusOK, apiRunAccess{Endpoint: url + dataProxyPath, Token: token, Expires: expires})
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
	LastUse time.Time `json:"last_use,omitzero"`
	Problem string    `json:"problem,omitempty"`
}

func (s *hiServer) exposeAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/expose", func(w http.ResponseWriter, _ *http.Request) {
		e := &s.exposure
		e.mu.Lock()
		defer e.mu.Unlock()
		writeJSON(w, http.StatusOK, apiExposure{Listen: e.listen, URL: e.url, Started: e.started, Until: e.until, LastUse: e.lastUse, Problem: e.problem})
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
		return nil
	case len(positional) == 1 && positional[0] == "stop":
		if err := adminCall(*dirFlag, http.MethodPost, "/admin/expose/stop", nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Stopped the exposure. Runs using it fail until they ask again.")
		return nil
	}
	return usageError{"usage: hi server expose [stop]"}
}
