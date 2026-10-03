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
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// A box's only way out is this proxy, which hi runs in a second container
// on the box's internal network and on a normal one. It allows CONNECT
// tunnels (and plain HTTP) to the hosts in an allowlist file, which it
// rereads when it changes, and logs every decision. A second listener
// passes Claude Code's requests to api.anthropic.com and puts the real
// token in place of the box's placeholder, so the token never enters the
// box.

const (
	boxProxyPort       = 3128
	boxInjectPort      = 3129
	boxDataPort        = 3130
	boxClaudePlacehold = "hi-box-placeholder"
)

// boxAllowlist is the set of hosts a box may reach. "*" allows everything.
type boxAllowlist struct {
	path    string
	mu      sync.Mutex
	modTime time.Time
	hosts   []string
}

func (a *boxAllowlist) reload() {
	info, err := os.Stat(a.path)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if info.ModTime().Equal(a.modTime) && a.hosts != nil {
		return
	}
	data, err := os.ReadFile(a.path)
	if err != nil {
		return
	}
	a.hosts = parseBoxAllowlist(string(data))
	a.modTime = info.ModTime()
}

func parseBoxAllowlist(text string) []string {
	hosts := []string{}
	for _, line := range strings.Split(text, "\n") {
		line, _, _ = strings.Cut(line, "#")
		if line = strings.ToLower(strings.TrimSpace(line)); line != "" {
			hosts = append(hosts, strings.TrimPrefix(strings.TrimPrefix(line, "*."), "."))
		}
	}
	return hosts
}

// allows matches a host and its subdomains against the list.
func (a *boxAllowlist) allows(host string) bool {
	a.reload()
	a.mu.Lock()
	defer a.mu.Unlock()
	return boxHostAllowed(a.hosts, host)
}

func boxHostAllowed(hosts []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, allowed := range hosts {
		if allowed == "*" || host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}

// boxNetworkLog appends one line per decision.
type boxNetworkLog struct {
	mu   sync.Mutex
	file io.Writer
}

func (l *boxNetworkLog) record(decision, host string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.file, "%s %s %s\n", time.Now().UTC().Format(time.RFC3339), decision, host)
}

// runBoxProxy is `hi box __proxy`, run inside the proxy container.
func runBoxProxy(args []string, stderr io.Writer) int {
	if len(args) != 3 && len(args) != 5 {
		fmt.Fprintln(stderr, "usage: hi box __proxy <allowlist> <log> <claude credentials or -> [<data token> <server's /hf URL>]")
		return 2
	}
	allow := &boxAllowlist{path: args[0]}
	logFile, err := os.OpenFile(args[1], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(stderr, "hi: %v\n", err)
		return 1
	}
	log := &boxNetworkLog{file: logFile}
	errs := make(chan error, 2)
	go func() {
		errs <- http.ListenAndServe(fmt.Sprintf(":%d", boxProxyPort), &boxEgressProxy{allow: allow, log: log})
	}()
	if args[2] != "-" {
		go func() {
			errs <- http.ListenAndServe(fmt.Sprintf(":%d", boxInjectPort), newBoxClaudeInjector(args[2], "https://api.anthropic.com", log))
		}()
	}
	if len(args) == 5 {
		go func() {
			errs <- http.ListenAndServe(fmt.Sprintf(":%d", boxDataPort), newBoxDataInjector(args[3], args[4], log))
		}()
	}
	fmt.Fprintf(stderr, "hi box proxy listening on :%d\n", boxProxyPort)
	err = <-errs
	fmt.Fprintf(stderr, "hi: %v\n", err)
	return 1
}

// boxEgressProxy is the CONNECT and plain-HTTP proxy.
type boxEgressProxy struct {
	allow *boxAllowlist
	log   *boxNetworkLog
	// dial is replaced in tests.
	dial func(ctx context.Context, network, address string) (net.Conn, error)
}

func (p *boxEgressProxy) dialer() func(ctx context.Context, network, address string) (net.Conn, error) {
	if p.dial != nil {
		return p.dial
	}
	return (&net.Dialer{Timeout: 15 * time.Second}).DialContext
}

func (p *boxEgressProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Hostname()
	if r.Method == http.MethodConnect {
		host, _, _ = net.SplitHostPort(r.Host)
	}
	if host == "" || !p.allow.allows(host) {
		p.log.record("blocked", host)
		http.Error(w, fmt.Sprintf("hi box: %s is not allowed; on the host run: hi box allow <name> %s", host, host), http.StatusForbidden)
		return
	}
	p.log.record("allowed", host)
	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	// Plain HTTP: forward the request as it is.
	if r.URL.Scheme != "http" {
		http.Error(w, "hi box: only http:// requests and CONNECT tunnels", http.StatusBadRequest)
		return
	}
	transport := &http.Transport{DialContext: p.dialer(), Proxy: nil}
	r.RequestURI = ""
	response, err := transport.RoundTrip(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for name, values := range response.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	io.Copy(w, response.Body)
}

func (p *boxEgressProxy) tunnel(w http.ResponseWriter, r *http.Request) {
	upstream, err := p.dialer()(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	go func() {
		// Bytes the client sent after the CONNECT line are already buffered.
		if buffered.Reader.Buffered() > 0 {
			io.CopyN(upstream, buffered, int64(buffered.Reader.Buffered()))
		}
		io.Copy(upstream, client)
		upstream.Close()
	}()
	io.Copy(client, upstream)
	client.Close()
}

// newBoxClaudeInjector passes Claude Code's API requests on with the real
// token. secret is a file with a token from `claude setup-token`, or Claude
// Code's .credentials.json, read again for each request so a refresh on
// the host is picked up.
func newBoxClaudeInjector(secret, upstream string, log *boxNetworkLog) http.Handler {
	target, _ := url.Parse(upstream)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Host = target.Host
		token, err := readBoxClaudeToken(secret)
		if err != nil {
			// The request goes on with the placeholder and fails; the log
			// says why.
			log.record("claude-sign-in-problem", err.Error())
		}
		if auth := r.Header.Get("Authorization"); strings.Contains(auth, boxClaudePlacehold) && token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Del("X-Api-Key")
		log.record("claude", r.URL.Path)
	}
	return proxy
}

func readBoxClaudeToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("the Claude sign-in can't be read: %w", err)
	}
	text := strings.TrimSpace(string(data))
	if !strings.HasPrefix(text, "{") {
		if text == "" {
			return "", errors.New("the Claude token file is empty")
		}
		return text, nil
	}
	var credentials struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &credentials); err != nil || credentials.ClaudeAiOauth.AccessToken == "" {
		return "", errors.New("no Claude sign-in in the credentials file; run claude on the host and sign in")
	}
	if expires := credentials.ClaudeAiOauth.ExpiresAt; expires > 0 && time.Now().UnixMilli() > expires {
		return credentials.ClaudeAiOauth.AccessToken, errors.New("the host's Claude sign-in has expired; run claude once on the host, or store a long-lived token with hi box token")
	}
	return credentials.ClaudeAiOauth.AccessToken, nil
}

// boxReadLines reads a small text file as lines, for logs.
func boxReadLines(path string, last int) []string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) > last {
		lines = lines[len(lines)-last:]
	}
	return lines
}
