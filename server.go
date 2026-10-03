package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// hi server brokers compute for connected devices: it holds the provider
// keys, makes every provider call itself, and starts nothing without an
// approval. See docs/specs/approved/hi_server.md.

const (
	// A replacement for sold-out hardware may cost up to this many times the
	// approved price, or the approved price plus this many dollars an hour,
	// whichever is higher, unless config.json says otherwise. Cheap hardware
	// gets a lot of room; expensive hardware stays near 2x.
	serverFallbackFactor = 2.0
	serverFallbackExtra  = 1.0
	serverDefaultPort    = 7373
	serverClockSkew      = 5 * time.Minute
	serverRequestExpiry  = 30 * time.Minute
	serverMaxBody        = 1 << 20
)

var (
	serverReconcileEvery = 30 * time.Second
	// serverProviderFactories are the providers a server can manage. Colab is
	// never managed: it runs under each user's own Google sign-in.
	serverProviderFactories = map[string]func() computeProvider{
		"runpod":    func() computeProvider { return newRunpodProvider() },
		"shadeform": func() computeProvider { return newShadeformProvider() },
	}
	// serverKeyEnv is where each managed driver reads its key.
	serverKeyEnv = map[string]string{"runpod": "RUNPOD_API_KEY", "shadeform": "SHADEFORM_API_KEY"}
	// serverKeyCheck validates a key before the server stores it.
	serverKeyCheck = map[string]func(key string) error{
		"runpod":    func(key string) error { return runpodRequest(key, http.MethodGet, "/catalog/cpus", nil, nil) },
		"shadeform": func(key string) error { return shadeformRequest(key, http.MethodGet, "/sshkeys", nil, nil) },
	}
)

// ---------------------------------------------------------------------------
// state

type serverState struct {
	Users    map[string]*serverUser    `json:"users"`
	Devices  map[string]*serverDevice  `json:"devices"`
	Requests map[string]*serverRequest `json:"requests"`
	Leases   map[string]*serverLease   `json:"leases"`
	// Alerts records budget alerts already sent, per month.
	Alerts map[string]bool `json:"alerts,omitempty"`
	// TemplateSources are the repositories of templates and skills served
	// to devices (server_templates.go).
	TemplateSources map[string]*templateSource `json:"template_sources,omitempty"`
	// Reported records the last period each Slack report covered.
	Reported map[string]string `json:"reported,omitempty"`
}

type serverUser struct {
	Name  string    `json:"name"`
	Group string    `json:"group"`
	Added time.Time `json:"added"`
	// Kind is "agent" for an agent that runs on its own; Owner is the
	// person responsible for it.
	Kind  string `json:"kind,omitempty"`
	Owner string `json:"owner,omitempty"`
}

type serverDevice struct {
	Fingerprint string    `json:"fingerprint"`
	PublicKey   string    `json:"public_key"`
	User        string    `json:"user"`
	Hostname    string    `json:"hostname"`
	Added       time.Time `json:"added"`
	LastSeen    time.Time `json:"last_seen"`
	// Version is the hi version the device last connected with.
	Version string `json:"version,omitempty"`
}

// serverRequest is an enrollment ("enroll") or a compute start ("compute").
// Compute requests move pending → starting → running → stopped, or end as
// denied, expired, or failed.
type serverRequest struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	State    string    `json:"state"`
	User     string    `json:"user"`
	Device   string    `json:"device"`
	Hostname string    `json:"hostname,omitempty"`
	Created  time.Time `json:"created"`
	Provider string    `json:"provider,omitempty"`
	Hardware string    `json:"hardware,omitempty"`
	Rate     string    `json:"rate,omitempty"`
	// Approved is the hardware as approved, when a sold-out start used a
	// replacement within the approval's bounds.
	Approved   string    `json:"approved,omitempty"`
	Name       string    `json:"name,omitempty"`
	MaxSeconds int64     `json:"max_seconds,omitempty"`
	Image      string    `json:"image,omitempty"`
	PublicKey  string    `json:"public_key,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	DecidedBy  string    `json:"decided_by,omitempty"`
	DecidedAt  time.Time `json:"decided_at,omitzero"`
	DenyReason string    `json:"deny_reason,omitempty"`
	Group      string    `json:"group,omitempty"`
	Progress   string    `json:"progress,omitempty"`
	Error      string    `json:"error,omitempty"`
	StoppedBy  string    `json:"stopped_by,omitempty"`
	Started    time.Time `json:"started,omitzero"`
	Ended      time.Time `json:"ended,omitzero"`
	// SlackTS is the request's message in the approvals channel.
	SlackTS string `json:"slack_ts,omitempty"`
	// Agent names the coding agent that sent it for the user, if any.
	Agent string `json:"agent,omitempty"`
	// Budget is the user's and group's spend this month when it was sent.
	Budget     string `json:"budget,omitempty"`
	OverBudget bool   `json:"over_budget,omitempty"`
	// Owner is the person responsible for an agent that enrolls itself.
	// A request of kind "extend" asks for MaxSeconds more on the running
	// instance Name.
	Owner string `json:"owner,omitempty"`
}

// serverLease is a running instance the server started, and the limits it
// enforces on it.
type serverLease struct {
	Name     string    `json:"name"`
	Provider string    `json:"provider"`
	Hardware string    `json:"hardware"`
	Rate     string    `json:"rate"`
	User     string    `json:"user"`
	Device   string    `json:"device"`
	Request  string    `json:"request"`
	Started  time.Time `json:"started"`
	Deadline time.Time `json:"deadline"`
	State    string    `json:"state,omitempty"`
	// Warned is set once the thread has been told the limit is near.
	Warned bool `json:"warned,omitempty"`
}

type hiServer struct {
	dir       string
	mu        sync.Mutex
	state     serverState
	keys      map[string]string
	providers map[string]computeProvider
	nonces    map[string]time.Time
	unleased  map[string]bool
	log       io.Writer
	// fallback bounds replacements for sold-out hardware.
	fallback priceBound
	// slack is the Slack bridge, or nil when Slack isn't set up.
	slack *slackBridge
	// feed is recent activity for the live dashboard.
	feed liveFeed
	// serverKey signs what devices must trust, such as template bundles.
	serverKey ed25519.PrivateKey
	// templateMu serializes git work on the template mirrors.
	templateMu sync.Mutex
	// dataMu guards the hi data listings and data_usage.jsonl.
	dataMu    sync.Mutex
	dataLists map[string]*dataListing
}

func serverDirectory(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if dir := os.Getenv("HI_SERVER_DIR"); dir != "" {
		return dir, nil
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "hi", "server"), nil
}

type serverConfig struct {
	Listen string `json:"listen"`
	// FallbackPriceFactor and FallbackPriceExtra bound replacements for
	// sold-out hardware: an approval covers free hardware with at least as
	// much memory costing up to factor times the approved price, or the
	// price plus extra dollars an hour, whichever is higher. A factor of 1
	// and an extra of 0 turn replacements off.
	FallbackPriceFactor float64  `json:"fallback_price_factor,omitempty"`
	FallbackPriceExtra  *float64 `json:"fallback_price_extra,omitempty"`
}

// priceBound is how much a replacement for sold-out hardware may cost.
type priceBound struct {
	factor float64
	extra  float64
}

// ceiling is the highest hourly price a replacement for price may have.
func (b priceBound) ceiling(price float64) float64 {
	return max(price*b.factor, price+b.extra)
}

func (b priceBound) enabled() bool { return b.factor > 1 || b.extra > 0 }

// fallbackProvider can suggest replacements for sold-out hardware: free
// hardware of the same kind with at least as much memory, costing at most
// ceiling dollars an hour, cheapest first.
type fallbackProvider interface {
	alternatives(name string, ceiling float64) ([]computeHardware, error)
}

func openServer(dir string, log io.Writer) (*hiServer, error) {
	server := &hiServer{
		dir:       dir,
		keys:      map[string]string{},
		providers: map[string]computeProvider{},
		nonces:    map[string]time.Time{},
		unleased:  map[string]bool{},
		log:       log,

		fallback: priceBound{factor: serverFallbackFactor, extra: serverFallbackExtra},
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, &server.state); err != nil {
			return nil, fmt.Errorf("read %s: %w", filepath.Join(dir, "state.json"), err)
		}
	}
	if server.state.Users == nil {
		server.state.Users = map[string]*serverUser{}
	}
	if server.state.Devices == nil {
		server.state.Devices = map[string]*serverDevice{}
	}
	if server.state.Requests == nil {
		server.state.Requests = map[string]*serverRequest{}
	}
	if server.state.Leases == nil {
		server.state.Leases = map[string]*serverLease{}
	}
	if server.state.TemplateSources == nil {
		server.state.TemplateSources = map[string]*templateSource{}
	}
	if server.serverKey, err = loadServerKey(dir); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(filepath.Join(dir, "keys.json")); err == nil {
		if err := json.Unmarshal(data, &server.keys); err != nil {
			return nil, fmt.Errorf("read the provider keys: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for name, key := range server.keys {
		server.enableProvider(name, key)
	}
	server.feed.loadRecentAudit(dir)
	// A start interrupted by a restart can't be resumed. If the instance was
	// created, the reconciler reports it as unleased.
	for _, request := range server.state.Requests {
		if request.State == "starting" {
			request.State = "failed"
			request.Error = "the server restarted while starting it; check `hi server ls` and the audit log"
		}
	}
	return server, nil
}

// enableProvider puts the key where the driver reads it and registers the
// driver. The key never leaves this process except to the provider.
func (s *hiServer) enableProvider(name, key string) {
	factory, ok := serverProviderFactories[name]
	if !ok {
		return
	}
	if env := serverKeyEnv[name]; env != "" {
		os.Setenv(env, key)
	}
	s.providers[name] = factory()
}

func (s *hiServer) saveLocked() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, "state.json")
	if err := os.WriteFile(path+".tmp", append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func (s *hiServer) saveKeysLocked() error {
	data, err := json.MarshalIndent(s.keys, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, "keys.json")
	if err := os.WriteFile(path+".tmp", append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

type auditEntry struct {
	Time    time.Time `json:"time"`
	Actor   string    `json:"actor"`
	Action  string    `json:"action"`
	Subject string    `json:"subject"`
	Detail  string    `json:"detail,omitempty"`
}

// audit appends one line to the audit log. It never records keys.
func (s *hiServer) audit(actor, action, subject, detail string) {
	detail = strings.Join(strings.Fields(strings.ReplaceAll(detail, "\n", " · ")), " ")
	entry := auditEntry{Time: computeNow().UTC(), Actor: actor, Action: action, Subject: subject, Detail: detail}
	s.feed.add(auditEvent(entry))
	data, _ := json.Marshal(entry)
	file, err := os.OpenFile(filepath.Join(s.dir, "audit.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		file.Write(append(data, '\n'))
		file.Close()
	}
	if s.log != nil {
		line := fmt.Sprintf("%s %s %s", actor, action, subject)
		if detail != "" {
			line += ": " + detail
		}
		fmt.Fprintln(s.log, line)
	}
}

func newServerID(prefix string) string {
	suffix := make([]byte, 3)
	rand.Read(suffix)
	return prefix + "-" + hex.EncodeToString(suffix)
}

// ---------------------------------------------------------------------------
// request signing

// Every client request is signed with the device's ed25519 key over the
// method, path, time, a nonce, and the body's hash, so it can't be forged or
// replayed. NetBird's encryption is a second layer, not the only one.

const (
	headerKey       = "Hi-Key"
	headerTime      = "Hi-Time"
	headerNonce     = "Hi-Nonce"
	headerSignature = "Hi-Signature"
)

func signingPayload(method, path, timestamp, nonce string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(strings.Join([]string{method, path, timestamp, nonce, hex.EncodeToString(sum[:])}, "\n"))
}

func keyFingerprint(public ed25519.PublicKey) string {
	sum := sha256.Sum256(public)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

func signRequest(request *http.Request, key ed25519.PrivateKey, body []byte) {
	timestamp := strconv.FormatInt(computeNow().Unix(), 10)
	nonceBytes := make([]byte, 16)
	rand.Read(nonceBytes)
	nonce := hex.EncodeToString(nonceBytes)
	signature := ed25519.Sign(key, signingPayload(request.Method, request.URL.RequestURI(), timestamp, nonce, body))
	request.Header.Set(headerKey, base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)))
	request.Header.Set(headerTime, timestamp)
	request.Header.Set(headerNonce, nonce)
	request.Header.Set(headerSignature, base64.StdEncoding.EncodeToString(signature))
}

// verifyRequest checks the signature and returns the key and its body.
func (s *hiServer) verifyRequest(request *http.Request, limit int64) (ed25519.PublicKey, []byte, error) {
	body, err := io.ReadAll(io.LimitReader(request.Body, limit))
	if err != nil {
		return nil, nil, err
	}
	public, err := base64.StdEncoding.DecodeString(request.Header.Get(headerKey))
	if err != nil || len(public) != ed25519.PublicKeySize {
		return nil, nil, errors.New("missing or malformed device key")
	}
	signature, err := base64.StdEncoding.DecodeString(request.Header.Get(headerSignature))
	if err != nil {
		return nil, nil, errors.New("malformed signature")
	}
	timestamp, nonce := request.Header.Get(headerTime), request.Header.Get(headerNonce)
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || nonce == "" || len(nonce) > 64 {
		return nil, nil, errors.New("missing time or nonce")
	}
	now := computeNow()
	if skew := now.Sub(time.Unix(seconds, 0)); skew > serverClockSkew || skew < -serverClockSkew {
		return nil, nil, errors.New("the request time is too far from the server's clock; check this machine's time")
	}
	if !ed25519.Verify(public, signingPayload(request.Method, request.URL.RequestURI(), timestamp, nonce, body), signature) {
		return nil, nil, errors.New("bad signature")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for seen, expires := range s.nonces {
		if now.After(expires) {
			delete(s.nonces, seen)
		}
	}
	if _, used := s.nonces[nonce]; used {
		return nil, nil, errors.New("replayed request")
	}
	s.nonces[nonce] = now.Add(2 * serverClockSkew)
	return ed25519.PublicKey(public), body, nil
}

// ---------------------------------------------------------------------------
// client API

type apiError struct {
	Error  string `json:"error"`
	Enroll string `json:"enroll,omitempty"`
}

type apiProvider struct {
	Name        string `json:"name"`
	MaxLifetime int64  `json:"max_lifetime_seconds,omitempty"`
}

type apiMe struct {
	User      string        `json:"user"`
	Group     string        `json:"group"`
	Device    string        `json:"device"`
	Providers []apiProvider `json:"providers"`
	// Budget is this month's spend against the user's and group's budgets.
	Budget     string `json:"budget,omitempty"`
	OverBudget bool   `json:"over_budget,omitempty"`
	// ServerKey is the server's public signing key, which devices store at
	// the first connection and check template bundles against.
	ServerKey string `json:"server_key,omitempty"`
}

type apiHardware struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Memory string `json:"memory"`
	Rate   string `json:"rate"`
	Paid   bool   `json:"paid"`
	Note   string `json:"note,omitempty"`
}

type apiSSH struct {
	Options     []string `json:"options"`
	Destination string   `json:"destination"`
	Hint        string   `json:"hint,omitempty"`
	Warning     string   `json:"warning,omitempty"`
}

type apiEnroll struct {
	User     string `json:"user"`
	Hostname string `json:"hostname"`
	// Agent and Owner enroll an agent that runs on its own.
	Agent bool   `json:"agent,omitempty"`
	Owner string `json:"owner,omitempty"`
}

type apiComputeRequest struct {
	Provider   string `json:"provider"`
	Hardware   string `json:"hardware"`
	Name       string `json:"name"`
	MaxSeconds int64  `json:"max_seconds"`
	Image      string `json:"image,omitempty"`
	PublicKey  string `json:"public_key"`
	Reason     string `json:"reason"`
	Agent      string `json:"agent,omitempty"`
}

type apiExtend struct {
	Seconds int64  `json:"seconds"`
	Reason  string `json:"reason"`
	Agent   string `json:"agent,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, apiError{Error: message})
}

// clientHandler serves connected devices. Every route but enrollment needs
// an approved device.
func (s *hiServer) clientHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/enroll", s.handleEnroll)
	mux.HandleFunc("GET /v1/me", s.device(s.handleMe))
	mux.HandleFunc("GET /v1/hardware", s.device(s.handleHardware))
	mux.HandleFunc("POST /v1/requests", s.device(s.handleCreateRequest))
	mux.HandleFunc("GET /v1/requests", s.device(s.handleListRequests))
	mux.HandleFunc("GET /v1/requests/{id}", s.device(s.handleGetRequest))
	mux.HandleFunc("GET /v1/instances", s.device(s.handleListInstances))
	mux.HandleFunc("POST /v1/instances/{name}/stop", s.device(s.handleStopInstance))
	mux.HandleFunc("GET /v1/instances/{name}/ssh", s.device(s.handleSSH))
	mux.HandleFunc("POST /v1/instances/{name}/extend", s.device(s.handleExtend))
	mux.HandleFunc("GET /v1/live", s.device(s.handleLive))
	mux.HandleFunc("POST /v1/activity", s.device(s.handleActivity))
	mux.HandleFunc("POST /v1/slack-link", s.device(s.handleSlackLink))
	mux.HandleFunc("GET /v1/templates", s.device(s.handleTemplateCatalog))
	mux.HandleFunc("GET /v1/templates/{source}/{commit}", s.device(s.handleTemplateBundle))
	s.aiRoutes(mux)
	s.dataRoutes(mux)
	return mux
}

type deviceHandler func(w http.ResponseWriter, r *http.Request, device serverDevice, body []byte)

func (s *hiServer) device(next deviceHandler) http.HandlerFunc {
	return s.deviceLimited(serverMaxBody, next)
}

// deviceLimited is device with its own limit on the body's size.
func (s *hiServer) deviceLimited(limit int64, next deviceHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		public, body, err := s.verifyRequest(r, limit)
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, err.Error())
			return
		}
		fingerprint := keyFingerprint(public)
		s.mu.Lock()
		device, known := s.state.Devices[fingerprint]
		if known {
			if _, ok := s.state.Users[device.User]; !ok {
				known = false
			}
		}
		if !known {
			pending := ""
			for _, request := range s.state.Requests {
				if request.Kind == "enroll" && request.Device == fingerprint && request.State == "pending" {
					pending = request.ID
				}
			}
			s.mu.Unlock()
			message := "this device is not enrolled; run `hi connect`"
			if pending != "" {
				message = fmt.Sprintf("this device is waiting for approval (%s)", pending)
			}
			writeJSON(w, http.StatusForbidden, apiError{Error: message, Enroll: pending})
			return
		}
		device.LastSeen = computeNow()
		if agent := r.UserAgent(); strings.HasPrefix(agent, "hi/") && len(agent) < 40 {
			device.Version = strings.TrimPrefix(agent, "hi/")
		}
		copy := *device
		s.mu.Unlock()
		next(w, r, copy, body)
	}
}

func (s *hiServer) handleEnroll(w http.ResponseWriter, r *http.Request) {
	public, body, err := s.verifyRequest(r, serverMaxBody)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, err.Error())
		return
	}
	var enroll apiEnroll
	if err := json.Unmarshal(body, &enroll); err != nil || !validServerName(enroll.User) || len(enroll.Hostname) > 64 {
		writeAPIError(w, http.StatusBadRequest, "use a user name of lowercase letters, digits, dots, or hyphens")
		return
	}
	if enroll.Agent && !validServerName(enroll.Owner) {
		writeAPIError(w, http.StatusBadRequest, "an agent needs --owner, the person responsible for it")
		return
	}
	fingerprint := keyFingerprint(public)
	s.mu.Lock()
	defer s.mu.Unlock()
	if device, ok := s.state.Devices[fingerprint]; ok {
		if _, ok := s.state.Users[device.User]; ok {
			if device.User != enroll.User {
				writeAPIError(w, http.StatusConflict, fmt.Sprintf("this device is enrolled as %s", device.User))
				return
			}
			writeJSON(w, http.StatusOK, serverRequest{State: "approved", User: device.User, Kind: "enroll"})
			return
		}
	}
	for _, request := range s.state.Requests {
		if request.Kind == "enroll" && request.Device == fingerprint && request.State == "pending" {
			writeJSON(w, http.StatusAccepted, request)
			return
		}
	}
	request := &serverRequest{
		ID:        newServerID("e"),
		Kind:      "enroll",
		State:     "pending",
		User:      enroll.User,
		Device:    fingerprint,
		Hostname:  enroll.Hostname,
		PublicKey: base64.StdEncoding.EncodeToString(public),
		Created:   computeNow(),
	}
	if enroll.Agent {
		request.Agent, request.Owner = enroll.User, enroll.Owner
	}
	s.state.Requests[request.ID] = request
	if err := s.saveLocked(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(enroll.User, "requested enrollment", request.ID, fmt.Sprintf("%s from %s", fingerprint, enroll.Hostname))
	s.notifyRequest(request.ID)
	writeJSON(w, http.StatusAccepted, request)
}

func (s *hiServer) handleMe(w http.ResponseWriter, _ *http.Request, device serverDevice, _ []byte) {
	policy := s.policy()
	s.mu.Lock()
	user := s.state.Users[device.User]
	me := apiMe{User: user.Name, Group: user.Group, Device: device.Fingerprint,
		ServerKey: base64.StdEncoding.EncodeToString(s.serverKey.Public().(ed25519.PublicKey))}
	if budget := s.budgetLocked(policy, user.Name, computeNow()); budget.IsSet || budget.UserSpend > 0 {
		me.Budget, me.OverBudget = budget.text(), budget.over()
	}
	s.mu.Unlock()
	providers := s.providerSnapshot()
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		me.Providers = append(me.Providers, apiProvider{
			Name: name, MaxLifetime: int64(providers[name].maxLifetime() / time.Second),
		})
	}
	writeJSON(w, http.StatusOK, me)
}

// providerSnapshot copies the managed providers; admins can change them
// while the server runs.
func (s *hiServer) providerSnapshot() map[string]computeProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	providers := make(map[string]computeProvider, len(s.providers))
	for name, provider := range s.providers {
		providers[name] = provider
	}
	return providers
}

func (s *hiServer) provider(name string) (computeProvider, error) {
	s.mu.Lock()
	provider, ok := s.providers[name]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("this server does not manage %q", name)
	}
	return provider, nil
}

func (s *hiServer) handleHardware(w http.ResponseWriter, r *http.Request, _ serverDevice, _ []byte) {
	provider, err := s.provider(r.URL.Query().Get("provider"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, err.Error())
		return
	}
	var options []computeHardware
	query := r.URL.Query()
	switch {
	case query.Get("name") != "":
		var one computeHardware
		one, err = resolveHardware(provider, query.Get("name"))
		options = []computeHardware{one}
	case query.Get("community") == "1":
		lister, ok := provider.(interface {
			communityHardware() ([]computeHardware, error)
		})
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "--community applies to RunPod only")
			return
		}
		options, err = lister.communityHardware()
	default:
		options, err = provider.hardware()
	}
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}
	result := make([]apiHardware, len(options))
	for i, option := range options {
		result[i] = apiHardware{Name: option.name, Kind: option.kind, Memory: option.memory,
			Rate: option.rate, Paid: option.paid, Note: option.note}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *hiServer) handleCreateRequest(w http.ResponseWriter, _ *http.Request, device serverDevice, body []byte) {
	if s.isViewer(device.User) {
		writeAPIError(w, http.StatusForbidden, "a viewer can only watch")
		return
	}
	var input apiComputeRequest
	if err := json.Unmarshal(body, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "malformed request")
		return
	}
	provider, err := s.provider(input.Provider)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, err.Error())
		return
	}
	switch {
	case !validInstanceName(input.Name):
		writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("invalid name %q", input.Name))
		return
	case input.MaxSeconds <= 0:
		writeAPIError(w, http.StatusBadRequest, "managed instances need a time limit; set --max")
		return
	case strings.TrimSpace(input.Reason) == "":
		writeAPIError(w, http.StatusBadRequest, "a reason is required; approvers see it")
		return
	case len(input.Reason) > 500 || len(input.Image) > 200 || len(input.PublicKey) > 2000:
		writeAPIError(w, http.StatusBadRequest, "the reason, image, or SSH key is too long")
		return
	case strings.TrimSpace(input.PublicKey) == "" || strings.ContainsAny(input.PublicKey, "\n\r"):
		writeAPIError(w, http.StatusBadRequest, "an SSH public key is required; run `ssh-keygen -t ed25519`")
		return
	}
	if limit := provider.maxLifetime(); limit > 0 && time.Duration(input.MaxSeconds)*time.Second > limit {
		writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("--max exceeds the %s limit of %s", input.Provider, formatDuration(limit)))
		return
	}
	hardware, err := resolveHardware(provider, input.Hardware)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A pod already using the name, even one the server didn't start, would
	// make a later stop ambiguous.
	instances, err := provider.list()
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}
	for _, instance := range instances {
		if instance.name == input.Name {
			writeAPIError(w, http.StatusConflict, fmt.Sprintf("an instance named %q already exists; choose another --name", input.Name))
			return
		}
	}

	policy := s.policy()
	s.mu.Lock()
	if _, taken := s.state.Leases[input.Name]; taken || s.nameRequestedLocked(input.Name) {
		s.mu.Unlock()
		writeAPIError(w, http.StatusConflict, fmt.Sprintf("an instance named %q already exists; choose another --name", input.Name))
		return
	}
	hours := float64(input.MaxSeconds) / 3600
	autoApproved, budget, err := s.checkRequestLocked(policy, device.User, hardware.name, hardware.rate, hours)
	if err != nil {
		s.mu.Unlock()
		writeAPIError(w, http.StatusForbidden, err.Error())
		return
	}
	request := &serverRequest{
		ID:         newServerID("r"),
		Kind:       "compute",
		State:      "pending",
		User:       device.User,
		Device:     device.Fingerprint,
		Hostname:   device.Hostname,
		Created:    computeNow(),
		Provider:   input.Provider,
		Hardware:   hardware.name,
		Rate:       hardware.rate,
		Name:       input.Name,
		MaxSeconds: input.MaxSeconds,
		Image:      input.Image,
		PublicKey:  strings.TrimSpace(input.PublicKey),
		Reason:     strings.TrimSpace(input.Reason),
		Agent:      cleanAgentName(input.Agent),
	}
	if budget.IsSet {
		request.Budget, request.OverBudget = budget.text(), budget.over()
	}
	s.state.Requests[request.ID] = request
	err = s.saveLocked()
	copy := *request
	s.mu.Unlock()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(device.User, "requested compute", request.ID, describeServerRequest(&copy))
	if autoApproved != "" {
		// decide notifies Slack once, already approved.
		if decided, err := s.decide(copy.ID, autoApproved, true, "", ""); err == nil {
			copy = decided
		}
	} else {
		s.notifyRequest(copy.ID)
	}
	writeJSON(w, http.StatusAccepted, copy)
}

// cleanAgentName keeps an agent label short and plain.
func cleanAgentName(name string) string {
	name = strings.TrimSpace(name)
	if len(name) > 40 {
		name = name[:40]
	}
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == '<' || r == '>' || r == '&' {
			return -1
		}
		return r
	}, name)
}

// handleExtend asks for more time on one of the user's running instances.
// It goes through the same approval as a start.
func (s *hiServer) handleExtend(w http.ResponseWriter, r *http.Request, device serverDevice, body []byte) {
	if s.isViewer(device.User) {
		writeAPIError(w, http.StatusForbidden, "a viewer can only watch")
		return
	}
	var input apiExtend
	if err := json.Unmarshal(body, &input); err != nil || input.Seconds <= 0 {
		writeAPIError(w, http.StatusBadRequest, "say how much longer, such as 1h")
		return
	}
	if strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 500 {
		writeAPIError(w, http.StatusBadRequest, "a reason is required; approvers see it")
		return
	}
	lease, ok := s.ownLease(r.PathValue("name"), device.User)
	if !ok {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("you have no instance named %q", r.PathValue("name")))
		return
	}
	policy := s.policy()
	now := computeNow()
	total := lease.Deadline.Sub(lease.Started).Hours() + float64(input.Seconds)/3600
	s.mu.Lock()
	for _, request := range s.state.Requests {
		if request.Kind == "extend" && request.Name == lease.Name && request.State == "pending" {
			s.mu.Unlock()
			writeAPIError(w, http.StatusConflict, fmt.Sprintf("%s already asks for more time on %s", request.ID, lease.Name))
			return
		}
	}
	autoApproved, budget, err := s.checkRequestLocked(policy, device.User, lease.Hardware, lease.Rate, total)
	if err != nil {
		s.mu.Unlock()
		writeAPIError(w, http.StatusForbidden, err.Error())
		return
	}
	request := &serverRequest{
		ID: newServerID("x"), Kind: "extend", State: "pending", User: device.User, Device: device.Fingerprint,
		Hostname: device.Hostname, Created: now, Provider: lease.Provider, Hardware: lease.Hardware,
		Rate: lease.Rate, Name: lease.Name, MaxSeconds: input.Seconds, Reason: strings.TrimSpace(input.Reason),
		Agent: cleanAgentName(input.Agent),
	}
	if budget.IsSet {
		request.Budget, request.OverBudget = budget.text(), budget.over()
	}
	s.state.Requests[request.ID] = request
	err = s.saveLocked()
	copy := *request
	s.mu.Unlock()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(device.User, "asked for more time", copy.ID, fmt.Sprintf("%s by %s: %s", copy.Name,
		formatDuration(time.Duration(copy.MaxSeconds)*time.Second), copy.Reason))
	if autoApproved != "" {
		if decided, err := s.decide(copy.ID, autoApproved, true, "", ""); err == nil {
			copy = decided
		}
	} else {
		s.notifyRequest(copy.ID)
	}
	writeJSON(w, http.StatusAccepted, copy)
}

// nameRequestedLocked reports an open request that will use this name.
func (s *hiServer) nameRequestedLocked(name string) bool {
	for _, request := range s.state.Requests {
		if request.Kind == "compute" && request.Name == name && (request.State == "pending" || request.State == "starting") {
			return true
		}
	}
	return false
}

func describeServerRequest(request *serverRequest) string {
	image := ""
	if request.Image != "" {
		image = ", image " + request.Image
	}
	return fmt.Sprintf("%s/%s on %s (%s)%s, max %s: %s", request.Provider, request.Name, request.Hardware,
		request.Rate, image, formatDuration(time.Duration(request.MaxSeconds)*time.Second), request.Reason)
}

func (s *hiServer) handleListRequests(w http.ResponseWriter, _ *http.Request, device serverDevice, _ []byte) {
	s.mu.Lock()
	var mine []serverRequest
	for _, request := range s.state.Requests {
		if request.Kind == "compute" && request.User == device.User {
			mine = append(mine, *request)
		}
	}
	s.mu.Unlock()
	sort.Slice(mine, func(i, j int) bool { return mine[i].Created.After(mine[j].Created) })
	if len(mine) > 20 {
		mine = mine[:20]
	}
	writeJSON(w, http.StatusOK, mine)
}

func (s *hiServer) handleGetRequest(w http.ResponseWriter, r *http.Request, device serverDevice, _ []byte) {
	s.mu.Lock()
	request, ok := s.state.Requests[r.PathValue("id")]
	var copy serverRequest
	if ok {
		copy = *request
	}
	s.mu.Unlock()
	if !ok || copy.User != device.User {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("no request %q", r.PathValue("id")))
		return
	}
	writeJSON(w, http.StatusOK, copy)
}

func (s *hiServer) handleListInstances(w http.ResponseWriter, _ *http.Request, device serverDevice, _ []byte) {
	s.mu.Lock()
	var mine []serverLease
	for _, lease := range s.state.Leases {
		if lease.User == device.User {
			mine = append(mine, *lease)
		}
	}
	s.mu.Unlock()
	sort.Slice(mine, func(i, j int) bool { return mine[i].Name < mine[j].Name })
	writeJSON(w, http.StatusOK, mine)
}

// ownLease finds the named lease if it belongs to the device's user.
func (s *hiServer) ownLease(name, user string) (serverLease, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.state.Leases[name]
	if !ok || lease.User != user {
		return serverLease{}, false
	}
	return *lease, true
}

func (s *hiServer) handleStopInstance(w http.ResponseWriter, r *http.Request, device serverDevice, _ []byte) {
	lease, ok := s.ownLease(r.PathValue("name"), device.User)
	if !ok {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("you have no instance named %q", r.PathValue("name")))
		return
	}
	if err := s.stopLease(lease, device.User); err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"stopped": lease.Name})
}

func (s *hiServer) handleSSH(w http.ResponseWriter, r *http.Request, device serverDevice, _ []byte) {
	lease, ok := s.ownLease(r.PathValue("name"), device.User)
	if !ok {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("you have no instance named %q", r.PathValue("name")))
		return
	}
	provider, err := s.provider(lease.Provider)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, err.Error())
		return
	}
	target, err := provider.ssh(lease.Name)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}
	warning := target.warning
	if warning == "" && isCommunityHardware(lease.Hardware) {
		warning = communityWarning
	}
	writeJSON(w, http.StatusOK, apiSSH{Options: target.options, Destination: target.destination,
		Hint: "hi: the instance accepts the SSH key from ~/.ssh that hi sent with the request", Warning: warning})
}

// ---------------------------------------------------------------------------
// decisions, starts, and stops

type serverUsageError struct{ message string }

func (e serverUsageError) Error() string { return e.message }

// decide approves or denies a pending request. Nobody decides their own.
func (s *hiServer) decide(id, actor string, approve bool, group, reason string) (serverRequest, error) {
	s.mu.Lock()
	request, ok := s.state.Requests[id]
	if !ok {
		s.mu.Unlock()
		return serverRequest{}, serverUsageError{fmt.Sprintf("no request %q", id)}
	}
	if request.State != "pending" {
		state := request.State
		s.mu.Unlock()
		return serverRequest{}, serverUsageError{fmt.Sprintf("%s is already %s", id, state)}
	}
	if actor == request.User {
		s.mu.Unlock()
		return serverRequest{}, serverUsageError{"nobody can approve or deny their own request; ask another approver " +
			"(an admin adds their own device with `hi server user add <user> --group G --key K`)"}
	}
	if approve && request.Kind == "enroll" {
		if _, exists := s.state.Users[request.User]; !exists && group == "" {
			s.mu.Unlock()
			return serverRequest{}, serverUsageError{fmt.Sprintf("%s is a new user; choose a group with --group", request.User)}
		}
	}
	request.DecidedBy, request.DecidedAt = actor, computeNow()
	if !approve {
		request.State, request.DenyReason = "denied", reason
		err := s.saveLocked()
		copy := *request
		s.mu.Unlock()
		s.audit(actor, "denied", id, reason)
		s.notifyRequest(id)
		if copy.Kind != "enroll" {
			text := fmt.Sprintf("Your request %s for `%s` was denied by %s.", id, copy.Name, actor)
			if reason != "" {
				text += " Reason: " + reason
			}
			s.notifyUser(copy.User, text)
		}
		return copy, err
	}
	if request.Kind == "enroll" {
		user, exists := s.state.Users[request.User]
		if !exists {
			user = &serverUser{Name: request.User, Group: group, Added: computeNow()}
			if request.Owner != "" {
				user.Kind, user.Owner = "agent", request.Owner
			}
			s.state.Users[request.User] = user
		}
		request.State, request.Group = "approved", user.Group
		s.state.Devices[request.Device] = &serverDevice{
			Fingerprint: request.Device, PublicKey: request.PublicKey, User: request.User,
			Hostname: request.Hostname, Added: computeNow(),
		}
		err := s.saveLocked()
		copy := *request
		s.mu.Unlock()
		s.audit(actor, "approved enrollment", id, fmt.Sprintf("%s (%s) on %s", request.User, user.Group, request.Hostname))
		s.notifyRequest(id)
		return copy, err
	}
	if request.Kind == "extend" {
		lease, running := s.state.Leases[request.Name]
		if !running {
			request.State, request.Error = "failed", request.Name+" is no longer running"
			err := s.saveLocked()
			copy := *request
			s.mu.Unlock()
			s.notifyRequest(id)
			return copy, err
		}
		lease.Deadline = lease.Deadline.Add(time.Duration(request.MaxSeconds) * time.Second)
		lease.Warned = false
		request.State = "approved"
		deadline := lease.Deadline
		original := lease.Request
		err := s.saveLocked()
		copy := *request
		s.mu.Unlock()
		s.audit(actor, "extended", request.Name, fmt.Sprintf("%s by %s, now stops at %s", id,
			formatDuration(time.Duration(request.MaxSeconds)*time.Second), deadline.Local().Format("15:04")))
		s.notifyRequest(id)
		s.notifyRequest(original)
		s.notifyThread(original, fmt.Sprintf("Extended by %s (%s, approved by %s). It now stops at %s.",
			formatDuration(time.Duration(request.MaxSeconds)*time.Second), id, actor, deadline.Local().Format("15:04")))
		return copy, err
	}
	request.State = "starting"
	err := s.saveLocked()
	copy := *request
	s.mu.Unlock()
	s.audit(actor, "approved", id, describeServerRequest(&copy))
	s.notifyRequest(id)
	s.notifyThread(id, fmt.Sprintf("Approved by %s. Starting it on %s: this usually takes 1–3 minutes, "+
		"and the main message turns 🟢 when it's ready.", actor, copy.Provider))
	go s.start(copy)
	return copy, err
}

// progressWriter keeps the last line a driver printed, for clients to show.
type progressWriter struct {
	server *hiServer
	id     string
}

func (p progressWriter) Write(data []byte) (int, error) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		p.server.mu.Lock()
		changed := false
		if request, ok := p.server.state.Requests[p.id]; ok && request.Progress != last {
			request.Progress, changed = last, true
		}
		p.server.mu.Unlock()
		if changed {
			p.server.notifyRequest(p.id)
		}
	}
	return len(data), nil
}

// start creates an approved instance and records its lease.
func (s *hiServer) start(request serverRequest) {
	fail := func(err error) {
		s.mu.Lock()
		if current, ok := s.state.Requests[request.ID]; ok {
			current.State, current.Error = "failed", err.Error()
		}
		s.saveLocked()
		s.mu.Unlock()
		s.audit("server", "failed to start", request.ID, err.Error())
		s.notifyRequest(request.ID)
	}
	provider, err := s.provider(request.Provider)
	if err != nil {
		fail(err)
		return
	}
	hardware, err := resolveHardware(provider, request.Hardware)
	if err != nil {
		fail(err)
		return
	}
	lifetime := time.Duration(request.MaxSeconds) * time.Second
	progress := progressWriter{server: s, id: request.ID}
	started := computeNow()
	create := func(hardware computeHardware) error {
		started = computeNow()
		return provider.create(upRequest{
			name: request.Name, hardware: hardware, max: lifetime, image: request.Image,
			publicKey: request.PublicKey, brokered: true,
		}, progress, progress)
	}
	err = create(hardware)
	// Sold out: the approval covers free hardware with at least as much
	// memory, up to the price bound.
	var soldOut noCapacityError
	if err != nil && errors.As(err, &soldOut) && s.fallback.enabled() {
		if fallback, ok := provider.(fallbackProvider); ok {
			ceiling := s.fallback.ceiling(hourlyRate(hardware.rate))
			alternatives, listErr := fallback.alternatives(hardware.name, ceiling)
			if listErr == nil && len(alternatives) == 0 {
				err = fmt.Errorf("%s is sold out, and nothing free with as much memory costs at most $%.2f/h",
					hardware.name, ceiling)
				// Say what is free at any price, so the next request can ask for it.
				if free, _ := fallback.alternatives(hardware.name, 1e6); len(free) > 0 {
					var names []string
					for i, option := range free {
						if i == 3 {
							break
						}
						names = append(names, fmt.Sprintf("%s (%s)", option.name, option.rate))
					}
					err = fmt.Errorf("%w. Free now with as much memory: %s", err, strings.Join(names, ", "))
				}
			}
			for i, alternative := range alternatives {
				if i == 3 {
					break
				}
				fmt.Fprintf(progress, "%s is sold out; starting %s (%s) instead\n", hardware.name, alternative.name, alternative.rate)
				s.audit("server", "replaced sold-out hardware", request.ID, fmt.Sprintf("%s (%s) with %s (%s), within $%.2f/h",
					hardware.name, hardware.rate, alternative.name, alternative.rate, ceiling))
				s.notifyThread(request.ID, fmt.Sprintf("%s was sold out; starting %s (%s) instead, within the approval's "+
					"bound of $%.2f/h.", hardware.name, alternative.name, alternative.rate, ceiling))
				if err = create(alternative); err == nil {
					s.mu.Lock()
					if current, ok := s.state.Requests[request.ID]; ok {
						current.Approved = fmt.Sprintf("%s (%s)", request.Hardware, request.Rate)
						current.Hardware, current.Rate = alternative.name, alternative.rate
					}
					s.mu.Unlock()
					request.Hardware, request.Rate = alternative.name, alternative.rate
					break
				}
				if !errors.As(err, &soldOut) {
					break
				}
			}
		}
	}
	if err != nil {
		fail(err)
		return
	}
	s.mu.Lock()
	s.state.Leases[request.Name] = &serverLease{
		Name: request.Name, Provider: request.Provider, Hardware: request.Hardware, Rate: request.Rate,
		User: request.User, Device: request.Device, Request: request.ID,
		Started: started, Deadline: started.Add(lifetime), State: "running",
	}
	if current, ok := s.state.Requests[request.ID]; ok {
		current.State, current.Progress, current.Started = "running", "", started
	}
	s.saveLocked()
	s.mu.Unlock()
	s.notifyRequest(request.ID)
	s.notifyThread(request.ID, fmt.Sprintf("Started on %s (%s). It stops at %s.", request.Hardware, request.Rate,
		started.Add(lifetime).Local().Format("15:04")))
	s.audit("server", "started", request.Name, fmt.Sprintf("%s on %s for %s, stops at %s", request.ID, request.Hardware,
		request.User, started.Add(lifetime).Local().Format("15:04")))
}

// stopLease terminates an instance and ends its lease.
func (s *hiServer) stopLease(lease serverLease, actor string) error {
	provider, err := s.provider(lease.Provider)
	if err != nil {
		return err
	}
	if err := provider.stop(lease.Name, io.Discard, io.Discard); err != nil {
		return fmt.Errorf("stop %s: %w", lease.Name, err)
	}
	s.endLease(lease.Name, actor)
	ran := formatDuration(computeNow().Sub(lease.Started))
	s.audit(actor, "stopped", lease.Name, fmt.Sprintf("ran %s, started by %s", ran, lease.User))
	if actor == "limit" {
		s.notifyThread(lease.Request, fmt.Sprintf("Reached its time limit after %s and was stopped.", ran))
	} else {
		s.notifyThread(lease.Request, fmt.Sprintf("Stopped by %s after %s.", actor, ran))
	}
	return nil
}

func (s *hiServer) endLease(name, stoppedBy string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.state.Leases[name]
	if !ok {
		return
	}
	delete(s.state.Leases, name)
	if request, ok := s.state.Requests[lease.Request]; ok {
		request.State, request.StoppedBy, request.Ended = "stopped", stoppedBy, computeNow()
		if request.Started.IsZero() {
			request.Started = lease.Started
		}
	}
	s.saveLocked()
	s.notifyRequest(lease.Request)
}

// reconcile expires old requests, stops instances past their limit, ends
// leases whose instance is gone, and reports instances with no lease.
func (s *hiServer) reconcile() {
	now := computeNow()
	s.mu.Lock()
	for _, request := range s.state.Requests {
		if request.State == "pending" && now.Sub(request.Created) > serverRequestExpiry {
			request.State = "expired"
			s.audit("server", "expired", request.ID, "no decision within "+formatDuration(serverRequestExpiry))
			s.notifyRequest(request.ID)
		}
	}
	starting := map[string]bool{}
	for _, request := range s.state.Requests {
		if request.State == "starting" {
			starting[request.Name] = true
		}
	}
	leases := make([]serverLease, 0, len(s.state.Leases))
	for _, lease := range s.state.Leases {
		leases = append(leases, *lease)
	}
	s.saveLocked()
	s.mu.Unlock()

	for name, provider := range s.providerSnapshot() {
		if forget, ok := provider.(interface{ forgetHardware() }); ok {
			forget.forgetHardware()
		}
		instances, err := provider.list()
		if err != nil {
			s.audit("server", "could not list", name, err.Error())
			continue
		}
		running := map[string]bool{}
		for _, instance := range instances {
			running[instance.name] = true
		}
		leased := map[string]bool{}
		for _, lease := range leases {
			if lease.Provider != name {
				continue
			}
			leased[lease.Name] = true
			switch {
			case !running[lease.Name] && now.Sub(lease.Started) > time.Minute:
				s.endLease(lease.Name, "gone")
				s.audit("server", "lease ended", lease.Name, "the instance is no longer running")
			case !now.Before(lease.Deadline):
				if err := s.stopLease(lease, "limit"); err != nil {
					s.audit("server", "could not stop", lease.Name, err.Error())
					s.notifyThread(lease.Request, "Could not stop it at its limit: "+err.Error())
				}
			case !lease.Warned && now.Sub(lease.Started) >= time.Duration(slackWarnAt*float64(lease.Deadline.Sub(lease.Started))):
				s.mu.Lock()
				if current, ok := s.state.Leases[lease.Name]; ok {
					current.Warned = true
					s.saveLocked()
				}
				s.mu.Unlock()
				s.notifyThread(lease.Request, fmt.Sprintf("%s has used %d%% of its time; it stops at %s.",
					lease.Name, int(slackWarnAt*100), lease.Deadline.Local().Format("15:04")))
			}
		}
		for _, instance := range instances {
			key := name + "/" + instance.name
			if leased[instance.name] || starting[instance.name] || s.unleased[key] {
				continue
			}
			s.unleased[key] = true
			s.audit("server", "unleased instance", key, "running on the organization's account without a request")
			s.notifyChannel(fmt.Sprintf("⚠️ `%s` is running on the organization's %s account without a request. "+
				"Stop it on the provider's website if nobody expects it.", instance.name, name))
		}
	}
	s.checkBudgets(now)
	s.maybeReport(now)
}

// ---------------------------------------------------------------------------
// admin API, over a Unix socket that only the server's user can open

func (s *hiServer) adminHandler() http.Handler {
	mux := http.NewServeMux()
	s.templateAdminRoutes(mux)
	s.dataAdminRoutes(mux)
	mux.HandleFunc("GET /admin/requests", func(w http.ResponseWriter, r *http.Request) {
		all := r.URL.Query().Get("all") == "1"
		s.mu.Lock()
		var list []serverRequest
		for _, request := range s.state.Requests {
			if all || request.State == "pending" || request.State == "starting" {
				list = append(list, *request)
			}
		}
		s.mu.Unlock()
		sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
		writeJSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("POST /admin/requests/{id}/{decision}", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			As     string `json:"as"`
			Group  string `json:"group"`
			Reason string `json:"reason"`
		}
		json.NewDecoder(io.LimitReader(r.Body, serverMaxBody)).Decode(&input)
		decision := r.PathValue("decision")
		if decision != "approve" && decision != "deny" {
			writeAPIError(w, http.StatusNotFound, "unknown decision")
			return
		}
		if input.As == "" {
			writeAPIError(w, http.StatusBadRequest, "say who is deciding with --as")
			return
		}
		request, err := s.decide(r.PathValue("id"), input.As, decision == "approve", input.Group, input.Reason)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, request)
	})
	mux.HandleFunc("GET /admin/instances", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		var list []serverLease
		for _, lease := range s.state.Leases {
			list = append(list, *lease)
		}
		s.mu.Unlock()
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		writeJSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("POST /admin/stop", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			As   string `json:"as"`
			Name string `json:"name"`
			User string `json:"user"`
			All  bool   `json:"all"`
		}
		json.NewDecoder(io.LimitReader(r.Body, serverMaxBody)).Decode(&input)
		s.mu.Lock()
		var chosen []serverLease
		for _, lease := range s.state.Leases {
			if input.All || (input.Name != "" && lease.Name == input.Name) || (input.User != "" && lease.User == input.User) {
				chosen = append(chosen, *lease)
			}
		}
		s.mu.Unlock()
		if len(chosen) == 0 {
			writeAPIError(w, http.StatusNotFound, "nothing matched; see `hi server ls`")
			return
		}
		var stopped, failed []string
		for _, lease := range chosen {
			if err := s.stopLease(lease, input.As); err != nil {
				failed = append(failed, err.Error())
			} else {
				stopped = append(stopped, lease.Name)
			}
		}
		writeJSON(w, http.StatusOK, map[string][]string{"stopped": stopped, "failed": failed})
	})
	mux.HandleFunc("GET /admin/users", func(w http.ResponseWriter, _ *http.Request) {
		type userRow struct {
			serverUser
			Devices []serverDevice `json:"devices"`
		}
		s.mu.Lock()
		var rows []userRow
		for _, user := range s.state.Users {
			row := userRow{serverUser: *user}
			for _, device := range s.state.Devices {
				if device.User == user.Name {
					row.Devices = append(row.Devices, *device)
				}
			}
			rows = append(rows, row)
		}
		s.mu.Unlock()
		sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
		writeJSON(w, http.StatusOK, rows)
	})
	mux.HandleFunc("POST /admin/users", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			As    string `json:"as"`
			Name  string `json:"name"`
			Group string `json:"group"`
			Key   string `json:"key"`
		}
		json.NewDecoder(io.LimitReader(r.Body, serverMaxBody)).Decode(&input)
		if !validServerName(input.Name) || input.Group == "" {
			writeAPIError(w, http.StatusBadRequest, "give a user name and --group")
			return
		}
		var public ed25519.PublicKey
		if input.Key != "" {
			var err error
			if public, err = decodeDeviceKey(input.Key); err != nil {
				writeAPIError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		s.mu.Lock()
		user, exists := s.state.Users[input.Name]
		if !exists {
			user = &serverUser{Name: input.Name, Added: computeNow()}
			s.state.Users[input.Name] = user
		}
		user.Group = input.Group
		if public != nil {
			fingerprint := keyFingerprint(public)
			s.state.Devices[fingerprint] = &serverDevice{Fingerprint: fingerprint, PublicKey: input.Key,
				User: input.Name, Hostname: "(added by an admin)", Added: computeNow()}
		}
		err := s.saveLocked()
		s.mu.Unlock()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.audit(input.As, "added user", input.Name, input.Group)
		writeJSON(w, http.StatusOK, user)
	})
	mux.HandleFunc("DELETE /admin/users/{name}", func(w http.ResponseWriter, r *http.Request) {
		name, as := r.PathValue("name"), r.URL.Query().Get("as")
		s.mu.Lock()
		if _, ok := s.state.Users[name]; !ok {
			s.mu.Unlock()
			writeAPIError(w, http.StatusNotFound, fmt.Sprintf("no user %q", name))
			return
		}
		delete(s.state.Users, name)
		for fingerprint, device := range s.state.Devices {
			if device.User == name {
				delete(s.state.Devices, fingerprint)
			}
		}
		err := s.saveLocked()
		s.mu.Unlock()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.audit(as, "removed user", name, "")
		writeJSON(w, http.StatusOK, map[string]string{"removed": name})
	})
	mux.HandleFunc("GET /admin/providers", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		names := make([]string, 0, len(s.providers))
		for name := range s.providers {
			names = append(names, name)
		}
		s.mu.Unlock()
		sort.Strings(names)
		writeJSON(w, http.StatusOK, names)
	})
	mux.HandleFunc("POST /admin/providers", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			As   string `json:"as"`
			Name string `json:"name"`
			Key  string `json:"key"`
		}
		json.NewDecoder(io.LimitReader(r.Body, serverMaxBody)).Decode(&input)
		if _, ok := serverProviderFactories[input.Name]; !ok {
			writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("%q can't be managed yet; managed providers: %s",
				input.Name, strings.Join(managedProviderNames(), ", ")))
			return
		}
		key := strings.TrimSpace(input.Key)
		if check := serverKeyCheck[input.Name]; check != nil {
			if err := check(key); err != nil {
				writeAPIError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		s.mu.Lock()
		s.keys[input.Name] = key
		s.enableProvider(input.Name, key)
		err := s.saveKeysLocked()
		s.mu.Unlock()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.audit(input.As, "set provider key", input.Name, "")
		writeJSON(w, http.StatusOK, map[string]string{"provider": input.Name})
	})
	mux.HandleFunc("DELETE /admin/providers/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		s.mu.Lock()
		delete(s.keys, name)
		delete(s.providers, name)
		if env := serverKeyEnv[name]; env != "" {
			os.Unsetenv(env)
		}
		err := s.saveKeysLocked()
		s.mu.Unlock()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.audit(r.URL.Query().Get("as"), "removed provider key", name, "")
		writeJSON(w, http.StatusOK, map[string]string{"removed": name})
	})
	s.approversHandler(mux)
	s.aiAdminRoutes(mux)
	mux.HandleFunc("GET /admin/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.liveSnapshot(""))
	})
	mux.HandleFunc("POST /admin/viewers", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			As   string `json:"as"`
			Name string `json:"name"`
			Key  string `json:"key"`
		}
		json.NewDecoder(io.LimitReader(r.Body, serverMaxBody)).Decode(&input)
		if err := s.addViewer(input.Name, input.Key, input.As); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"viewer": input.Name})
	})
	mux.HandleFunc("GET /admin/spend", func(w http.ResponseWriter, r *http.Request) {
		seconds, _ := strconv.ParseInt(r.URL.Query().Get("seconds"), 10, 64)
		to := computeNow()
		from := monthStart(to)
		if seconds > 0 {
			from = to.Add(-time.Duration(seconds) * time.Second)
		}
		writeJSON(w, http.StatusOK, map[string]string{"text": s.spendSummary(from, to)})
	})
	return mux
}

// spendSummary is spend per user and group between from and to, as text.
func (s *hiServer) spendSummary(from, to time.Time) string {
	policy := s.policy()
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, perGroup := s.spendTableLocked(policy, from, to)
	ai := ""
	if usage := readAIUsage(s.dir, from, to); len(usage) > 0 {
		ai = "\n\nModels through hi q: " + describeAIUsage(usage)
	}
	if len(rows) == 0 {
		return fmt.Sprintf("No compute was used since %s.", from.Local().Format("2 Jan 15:04")) + ai
	}
	var total float64
	for _, row := range rows {
		total += row.Spend
	}
	return fmt.Sprintf("Since %s: %s\n\nBy user\n%s\n\nBy group\n%s", from.Local().Format("2 Jan 15:04"),
		formatDollars(total), describeSpendRows(rows, 50), describeGroupSpend(policy, perGroup)) + ai
}

// decodeDeviceKey reads a device key as `hi connect key` prints it.
func decodeDeviceKey(key string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(key))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("that is not a device key; get it with `hi connect key` on the device")
	}
	return ed25519.PublicKey(raw), nil
}

// isViewer reports a device that may only watch the dashboard.
func (s *hiServer) isViewer(user string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.state.Users[user]
	return ok && record.Kind == "viewer"
}

func managedProviderNames() []string {
	names := make([]string, 0, len(serverProviderFactories))
	for name := range serverProviderFactories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validServerName(name string) bool {
	if len(name) == 0 || len(name) > 32 {
		return false
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' && char != '.' && char != '_' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// running

// serve listens for clients and admins until the context ends.
func (s *hiServer) serve(ctx context.Context, listen string) error {
	clientListener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listen, err)
	}
	socket := filepath.Join(s.dir, "admin.sock")
	os.Remove(socket)
	adminListener, err := net.Listen("unix", socket)
	if err != nil {
		clientListener.Close()
		return fmt.Errorf("open the admin socket: %w", err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		clientListener.Close()
		adminListener.Close()
		return err
	}
	client := &http.Server{Handler: s.clientHandler(), ReadHeaderTimeout: 10 * time.Second}
	admin := &http.Server{Handler: s.adminHandler(), ReadHeaderTimeout: 10 * time.Second}
	go client.Serve(clientListener)
	go admin.Serve(adminListener)
	fmt.Fprintf(s.log, "hi server %s listening on %s\n", version, listen)
	if err := s.startSlack(ctx); err != nil {
		fmt.Fprintf(s.log, "slack: %v\n", err)
	}

	ticker := time.NewTicker(serverReconcileEvery)
	defer ticker.Stop()
	templates := time.NewTicker(templateSyncEvery)
	defer templates.Stop()
	s.reconcile()
	s.syncTemplatesInBackground()
	for {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client.Shutdown(shutdown)
			admin.Shutdown(shutdown)
			os.Remove(socket)
			return nil
		case <-ticker.C:
			s.reconcile()
		case <-templates.C:
			s.syncTemplatesInBackground()
		}
	}
}

// netbirdAddress finds this machine's NetBird address, so the server
// listens inside the VPN only.
func netbirdAddress() (string, error) {
	iface, err := net.InterfaceByName("wt0")
	if err != nil {
		return "", errors.New("no NetBird interface (wt0) found; enroll with `hi net` or pass --listen")
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return "", err
	}
	for _, address := range addresses {
		if network, ok := address.(*net.IPNet); ok && network.IP.To4() != nil {
			return network.IP.String(), nil
		}
	}
	return "", errors.New("the NetBird interface has no IPv4 address; pass --listen")
}

// ---------------------------------------------------------------------------
// commands

func runServer(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printServerUsage(stdout)
		return 0
	}
	command, rest := args[0], args[1:]
	switch command {
	case "help", "-h", "--help":
		printServerUsage(stdout)
		return 0
	case "init":
		return exitCode(serverInitCommand(rest, stdout, stderr), stderr)
	case "run":
		return exitCode(serverRunCommand(rest, stdout, stderr), stderr)
	case "provider":
		return exitCode(serverProviderCommand(rest, stdin, stdout, stderr), stderr)
	case "user":
		return exitCode(serverUserCommand(rest, stdout, stderr), stderr)
	case "requests":
		return exitCode(serverRequestsCommand(rest, stdout, stderr), stderr)
	case "approve", "deny":
		return exitCode(serverDecideCommand(command, rest, stdout, stderr), stderr)
	case "ls":
		return exitCode(serverListCommand(rest, stdout, stderr), stderr)
	case "stop":
		return exitCode(serverStopCommand(rest, stdout, stderr), stderr)
	case "audit":
		return exitCode(serverAuditCommand(rest, stdout, stderr), stderr)
	case "slack":
		return exitCode(serverSlackCommand(rest, stdin, stdout, stderr), stderr)
	case "approvers":
		return exitCode(serverApproversCommand(rest, stdout, stderr), stderr)
	case "policy":
		return exitCode(serverPolicyCommand(rest, stdin, stdout, stderr), stderr)
	case "spend":
		return exitCode(serverSpendCommand(rest, stdout, stderr), stderr)
	case "ai":
		return exitCode(serverAICommand(rest, stdin, stdout, stderr), stderr)
	case "viewer":
		return exitCode(serverViewerCommand(rest, stdout, stderr), stderr)
	case "live":
		return exitCode(serverLiveCommand(rest, stdin, stdout, stderr), stderr)
	case "wall":
		return exitCode(serverWallCommand(rest, stdout, stderr), stderr)
	case "templates":
		return exitCode(serverTemplatesCommand(rest, stdin, stdout, stderr), stderr)
	case "data":
		return exitCode(serverDataCommand(rest, stdin, stdout, stderr), stderr)
	default:
		fmt.Fprintf(stderr, "hi: unknown server command %q\n\n", command)
		printServerUsage(stderr)
		return 2
	}
}

func printServerUsage(w io.Writer) {
	fmt.Fprintln(w, `hi server brokers compute for connected devices: it holds the provider keys,
and nothing starts without an approval. It also serves the team's private
project templates to hi init, and the team's Hugging Face datasets and
models to hi data.

Usage:
  hi server init [--listen ADDR]          Create the server's state here
  hi server run                           Serve clients (run it as a systemd service)
  hi server provider add|remove <p>       Store or remove a provider key (runpod)
  hi server provider list                 Show managed providers
  hi server user add <u> --group G [--key K]
                                          Add a user; --key pre-approves a device
  hi server user remove <u>               Remove a user and all their devices
  hi server user list                     Users, groups, and devices
  hi server requests [--all]              Pending requests (--all: every request)
  hi server approve <id> [--group G]      Approve an enrollment or compute request
  hi server deny <id> [--reason R]        Deny a request
  hi server ls                            Everything running, for every user
  hi server stop <name> | --user U | --all
                                          Stop instances
  hi server audit [--since 7d]            Requests, approvals, starts, and stops
  hi server slack setup                   Connect a Slack app (see: hi server slack manifest)
  hi server approvers add <Slack ID> --name <user>
                                          Let someone approve and stop from Slack
  hi server approvers remove <Slack ID> | list
  hi server policy show|edit|example|check
                                          Groups: limits, auto-approve, budgets
  hi server spend [--since 30d]           Spend per user and group, compute and models
  hi server ai [set|off|remove]           Pass hi q's model requests to OpenRouter with the team's key
  hi server live [--wall]                 Live dashboard; --wall is read-only for a shared screen
  hi server wall setup|add|remove|list    Show the wall dashboard on screens over SSH (needs sudo)
  hi server viewer add <name> --key K     A device that may only watch the dashboard
  hi server templates add <name> <git-url> [--ref R]
                                          Serve a private repository's templates and skills
  hi server templates list|sync|remove    Show, fetch now, or stop serving template sources
  hi server templates rename <name> <new> Rename a source; keeps its token and history
  hi server data                          Hugging Face organizations for hi data (menu)
  hi server data add <org>... [--token-file F]
                                          Serve an organization's datasets, models, and buckets
  hi server data list|test|remove         Show, check, or stop serving organizations

Commands other than init and run talk to the running server through its
admin socket, so they work only on the server box. Decisions are recorded
under your login name; change it with --as.

Options:
  --dir <dir>    State directory (default: $HI_SERVER_DIR or ~/.local/state/hi/server)`)
}

func serverFlags(name string, stderr io.Writer) (*flagSet, *string) {
	flags := newComputeFlags("server "+name, stderr)
	dir := flags.String("dir", "", "state directory")
	return &flagSet{flags}, dir
}

func serverInitCommand(args []string, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("init", stderr)
	listen := flags.String("listen", "", "address to listen on")
	if _, err := flags.parse(args); err != nil {
		return err
	}
	dir, err := serverDirectory(*dirFlag)
	if err != nil {
		return err
	}
	if *listen == "" {
		address, err := netbirdAddress()
		if err != nil {
			return err
		}
		*listen = net.JoinHostPort(address, strconv.Itoa(serverDefaultPort))
	}
	if _, _, err := net.SplitHostPort(*listen); err != nil {
		return usageError{fmt.Sprintf("--listen %q is not host:port", *listen)}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(serverConfig{Listen: *listen}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "config.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); errors.Is(err, os.ErrNotExist) {
		server, err := openServer(dir, io.Discard)
		if err != nil {
			return err
		}
		if err := server.saveLocked(); err != nil {
			return err
		}
	}
	executable, _ := os.Executable()
	fmt.Fprintf(stdout, "The server's state is in %s, readable only by you.\n", dir)
	fmt.Fprintf(stdout, "Clients connect with: hi connect %s\n\n", *listen)
	fmt.Fprintln(stdout, "Next:")
	fmt.Fprintln(stdout, "  hi server provider add runpod")
	fmt.Fprintln(stdout, "  hi server run          (or as a service, below)")
	fmt.Fprintf(stdout, "\nA systemd unit, for /etc/systemd/system/hi-server.service:\n\n")
	fmt.Fprintf(stdout, "[Unit]\nDescription=hi compute server\nAfter=network-online.target netbird.service\nWants=network-online.target\n\n")
	fmt.Fprintf(stdout, "[Service]\nUser=%s\nEnvironment=HI_SERVER_DIR=%s\nExecStart=%s server run\nRestart=always\n\n", currentUserName(), dir, executable)
	fmt.Fprintf(stdout, "[Install]\nWantedBy=multi-user.target\n")
	return nil
}

func serverRunCommand(args []string, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("run", stderr)
	if _, err := flags.parse(args); err != nil {
		return err
	}
	dir, err := serverDirectory(*dirFlag)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return fmt.Errorf("no server in %s; run `hi server init` first", dir)
	}
	var config serverConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return err
	}
	server, err := openServer(dir, stdout)
	if err != nil {
		return err
	}
	if config.FallbackPriceFactor != 0 {
		server.fallback.factor = config.FallbackPriceFactor
	}
	if config.FallbackPriceExtra != nil {
		server.fallback.extra = *config.FallbackPriceExtra
	}
	if len(server.providers) == 0 {
		fmt.Fprintln(stderr, "hi: warning: no provider keys yet; add one with `hi server provider add runpod`")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.serve(ctx, config.Listen)
}

// adminClient talks to the running server over its admin socket.
func adminClient(dir string) (*http.Client, error) {
	socket := filepath.Join(dir, "admin.sock")
	if _, err := os.Stat(socket); err != nil {
		return nil, fmt.Errorf("the server is not running in %s; start it with `hi server run`", dir)
	}
	return &http.Client{
		Timeout: 2 * time.Minute,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		}},
	}, nil
}

func adminCall(dirFlag, method, path string, body, result any) error {
	dir, err := serverDirectory(dirFlag)
	if err != nil {
		return err
	}
	client, err := adminClient(dir)
	if err != nil {
		return err
	}
	return doJSON(client, method, "http://hi-server"+path, body, result, nil)
}

// doJSON sends a JSON request, optionally signed, and decodes the reply or
// the server's error message.
func doJSON(client *http.Client, method, url string, body, result any, key ed25519.PrivateKey) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	request, err := http.NewRequest(method, url, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "hi/"+version)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != nil {
		signRequest(request, key, payload)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode >= 300 {
		var problem apiError
		if json.Unmarshal(data, &problem) == nil && problem.Error != "" {
			return &serverReplyError{status: response.StatusCode, message: problem.Error, enroll: problem.Enroll}
		}
		return &serverReplyError{status: response.StatusCode, message: strings.TrimSpace(string(data))}
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(data, result)
}

type serverReplyError struct {
	status  int
	message string
	enroll  string
}

func (e *serverReplyError) Error() string { return e.message }

func currentUserName() string {
	for _, name := range []string{os.Getenv("USER"), os.Getenv("LOGNAME")} {
		if name != "" {
			return name
		}
	}
	return "admin"
}

func serverProviderCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("provider", stderr)
	as := flags.String("as", currentUserName(), "who is acting")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	usage := usageError{"usage: hi server provider add|remove <provider> | list"}
	switch {
	case len(positional) == 1 && positional[0] == "list":
		var names []string
		if err := adminCall(*dirFlag, http.MethodGet, "/admin/providers", nil, &names); err != nil {
			return err
		}
		if len(names) == 0 {
			fmt.Fprintln(stdout, "No provider keys yet; add one with `hi server provider add runpod`.")
		}
		for _, name := range names {
			fmt.Fprintln(stdout, name)
		}
		return nil
	case len(positional) == 2 && positional[0] == "add":
		name := positional[1]
		if _, ok := serverProviderFactories[name]; !ok {
			return usageError{fmt.Sprintf("%q can't be managed yet; managed providers: %s",
				name, strings.Join(managedProviderNames(), ", "))}
		}
		dir, err := serverDirectory(*dirFlag)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
			return fmt.Errorf("no server in %s; run `hi server init` first", dir)
		}
		key, err := readProviderKey(name, stdin, stdout)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(dir, "admin.sock")); err == nil {
			body := map[string]string{"as": *as, "name": name, "key": key}
			if err := adminCall(*dirFlag, http.MethodPost, "/admin/providers", body, nil); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Connected devices can now use %s through this server.\n", name)
			return nil
		}
		// The server isn't running: check the key and store it for its next start.
		if check := serverKeyCheck[name]; check != nil {
			if err := check(key); err != nil {
				return err
			}
		}
		server, err := openServer(dir, io.Discard)
		if err != nil {
			return err
		}
		server.keys[name] = key
		if err := server.saveKeysLocked(); err != nil {
			return err
		}
		server.audit(*as, "set provider key", name, "")
		fmt.Fprintf(stdout, "Saved the %s key. Start the server with `hi server run`.\n", name)
		return nil
	case len(positional) == 2 && positional[0] == "remove":
		path := "/admin/providers/" + positional[1] + "?as=" + *as
		if err := adminCall(*dirFlag, http.MethodDelete, path, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Removed the %s key. Running instances keep running; stop them first if needed.\n", positional[1])
		return nil
	}
	return usage
}

// readProviderKey asks for a key without echo, or reads it from stdin in
// scripts. It never appears in arguments or output.
func readProviderKey(provider string, stdin io.Reader, stdout io.Writer) (string, error) {
	if file, ok := stdin.(*os.File); ok && isTerminal(stdin) {
		fmt.Fprintf(stdout, "%s API key (paste it; it shows as *): ", provider)
		key, err := readSecret(file, stdout)
		fmt.Fprintln(stdout)
		if err != nil {
			return "", err
		}
		if len(strings.TrimSpace(string(key))) == 0 {
			return "", errors.New("no key was entered")
		}
		fmt.Fprintf(stdout, "Got %d characters; checking the key with %s...\n", len(strings.TrimSpace(string(key))), provider)
		return strings.TrimSpace(string(key)), nil
	}
	line, err := readLine(stdin)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if strings.TrimSpace(line) == "" {
		return "", errors.New("no key given; run this in a terminal, or pipe the key on stdin")
	}
	return strings.TrimSpace(line), nil
}

func serverUserCommand(args []string, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("user", stderr)
	as := flags.String("as", currentUserName(), "who is acting")
	group := flags.String("group", "", "group")
	key := flags.String("key", "", "device key from `hi connect key`")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	switch {
	case len(positional) == 1 && positional[0] == "list":
		var rows []struct {
			Name    string         `json:"name"`
			Group   string         `json:"group"`
			Devices []serverDevice `json:"devices"`
		}
		if err := adminCall(*dirFlag, http.MethodGet, "/admin/users", nil, &rows); err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Fprintln(stdout, "No users yet.")
			return nil
		}
		table := newTable(stdout)
		fmt.Fprintln(table, "USER\tGROUP\tDEVICE\tLAST SEEN")
		for _, row := range rows {
			if len(row.Devices) == 0 {
				fmt.Fprintf(table, "%s\t%s\t-\t-\n", row.Name, row.Group)
			}
			for _, device := range row.Devices {
				seen := "never"
				if !device.LastSeen.IsZero() {
					seen = formatDuration(computeNow().Sub(device.LastSeen)) + " ago"
				}
				fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", row.Name, row.Group, device.Hostname, seen)
			}
		}
		return table.Flush()
	case len(positional) == 2 && positional[0] == "add":
		if *group == "" {
			return usageError{"usage: hi server user add <user> --group <group> [--key <device key>]"}
		}
		body := map[string]string{"as": *as, "name": positional[1], "group": *group, "key": *key}
		if err := adminCall(*dirFlag, http.MethodPost, "/admin/users", body, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Added %s to %s.\n", positional[1], *group)
		if *key != "" {
			fmt.Fprintln(stdout, "That device can connect without waiting for approval.")
		}
		return nil
	case len(positional) == 2 && positional[0] == "remove":
		if err := adminCall(*dirFlag, http.MethodDelete, "/admin/users/"+positional[1]+"?as="+*as, nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Removed %s and their devices. Stop their instances with `hi server stop --user %s`.\n",
			positional[1], positional[1])
		return nil
	}
	return usageError{"usage: hi server user add <u> --group G [--key K] | remove <u> | list"}
}

func serverRequestsCommand(args []string, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("requests", stderr)
	all := flags.Bool("all", false, "every request")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return usageError{"usage: hi server requests [--all]"}
	}
	path := "/admin/requests"
	if *all {
		path += "?all=1"
	}
	var requests []serverRequest
	if err := adminCall(*dirFlag, http.MethodGet, path, nil, &requests); err != nil {
		return err
	}
	if len(requests) == 0 {
		fmt.Fprintln(stdout, "Nothing is waiting for a decision.")
		return nil
	}
	table := newTable(stdout)
	fmt.Fprintln(table, "ID\tSTATE\tUSER\tWAITING\tWHAT")
	for _, request := range requests {
		what := fmt.Sprintf("join from %s", request.Hostname)
		if request.Kind == "compute" {
			what = describeServerRequest(&request)
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", request.ID, request.State, request.User,
			formatDuration(computeNow().Sub(request.Created)), what)
	}
	return table.Flush()
}

func serverDecideCommand(decision string, args []string, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags(decision, stderr)
	as := flags.String("as", currentUserName(), "who is deciding")
	group := flags.String("group", "", "group for a new user")
	reason := flags.String("reason", "", "why it is denied")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return usageError{fmt.Sprintf("usage: hi server %s <request id>", decision)}
	}
	body := map[string]string{"as": *as, "group": *group, "reason": *reason}
	var request serverRequest
	if err := adminCall(*dirFlag, http.MethodPost, "/admin/requests/"+positional[0]+"/"+decision, body, &request); err != nil {
		return err
	}
	switch {
	case decision == "deny":
		fmt.Fprintf(stdout, "Denied %s for %s.\n", request.ID, request.User)
	case request.Kind == "enroll":
		fmt.Fprintf(stdout, "Approved %s's device %s (%s).\n", request.User, request.Hostname, request.Group)
	default:
		fmt.Fprintf(stdout, "Approved %s; starting %s/%s for %s.\n", request.ID, request.Provider, request.Name, request.User)
	}
	return nil
}

func serverListCommand(args []string, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("ls", stderr)
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return usageError{"usage: hi server ls"}
	}
	var leases []serverLease
	if err := adminCall(*dirFlag, http.MethodGet, "/admin/instances", nil, &leases); err != nil {
		return err
	}
	if len(leases) == 0 {
		fmt.Fprintln(stdout, "Nothing is running.")
		return nil
	}
	now := computeNow()
	table := newTable(stdout)
	fmt.Fprintln(table, "NAME\tUSER\tPROVIDER\tHARDWARE\tRATE\tUP\tSTOPS IN")
	for _, lease := range leases {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", lease.Name, lease.User, lease.Provider, lease.Hardware,
			lease.Rate, formatDuration(now.Sub(lease.Started)), formatDuration(lease.Deadline.Sub(now)))
	}
	return table.Flush()
}

func serverStopCommand(args []string, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("stop", stderr)
	as := flags.String("as", currentUserName(), "who is acting")
	user := flags.String("user", "", "stop everything this user runs")
	all := flags.Bool("all", false, "stop everything")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	chosen := 0
	for _, set := range []bool{len(positional) == 1, *user != "", *all} {
		if set {
			chosen++
		}
	}
	if chosen != 1 || len(positional) > 1 {
		return usageError{"usage: hi server stop <name> | --user <user> | --all"}
	}
	body := map[string]any{"as": *as, "user": *user, "all": *all}
	if len(positional) == 1 {
		body["name"] = positional[0]
	}
	var result map[string][]string
	if err := adminCall(*dirFlag, http.MethodPost, "/admin/stop", body, &result); err != nil {
		return err
	}
	for _, name := range result["stopped"] {
		fmt.Fprintf(stdout, "Stopped %s.\n", name)
	}
	if failed := result["failed"]; len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

func serverAuditCommand(args []string, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("audit", stderr)
	since := flags.String("since", "7d", "how far back")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return usageError{"usage: hi server audit [--since 7d]"}
	}
	window, err := parseLifetime(*since)
	if err != nil || window == noLimit {
		return usageError{fmt.Sprintf("invalid --since %q; use e.g. 24h or 7d", *since)}
	}
	dir, err := serverDirectory(*dirFlag)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stdout, "Nothing has happened yet.")
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := computeNow().Add(-window)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry auditEntry
		if json.Unmarshal([]byte(line), &entry) != nil || entry.Time.Before(cutoff) {
			continue
		}
		text := fmt.Sprintf("%s  %s %s %s", entry.Time.Local().Format("2006-01-02 15:04"), entry.Actor, entry.Action, entry.Subject)
		if entry.Detail != "" {
			text += ": " + entry.Detail
		}
		fmt.Fprintln(stdout, text)
	}
	return nil
}
