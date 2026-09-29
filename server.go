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
	// serverFallbackFactor is how much more per hour a replacement may cost
	// when the approved hardware is sold out, unless config.json says.
	serverFallbackFactor = 2.0
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
		"runpod": func() computeProvider { return newRunpodProvider() },
	}
	// serverKeyEnv is where each managed driver reads its key.
	serverKeyEnv = map[string]string{"runpod": "RUNPOD_API_KEY"}
	// serverKeyCheck validates a key before the server stores it.
	serverKeyCheck = map[string]func(key string) error{
		"runpod": func(key string) error { return runpodRequest(key, http.MethodGet, "/catalog/cpus", nil, nil) },
	}
)

// ---------------------------------------------------------------------------
// state

type serverState struct {
	Users    map[string]*serverUser    `json:"users"`
	Devices  map[string]*serverDevice  `json:"devices"`
	Requests map[string]*serverRequest `json:"requests"`
	Leases   map[string]*serverLease   `json:"leases"`
}

type serverUser struct {
	Name  string    `json:"name"`
	Group string    `json:"group"`
	Added time.Time `json:"added"`
}

type serverDevice struct {
	Fingerprint string    `json:"fingerprint"`
	PublicKey   string    `json:"public_key"`
	User        string    `json:"user"`
	Hostname    string    `json:"hostname"`
	Added       time.Time `json:"added"`
	LastSeen    time.Time `json:"last_seen"`
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
	// fallbackFactor bounds replacements for sold-out hardware.
	fallbackFactor float64
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
	// FallbackPriceFactor bounds replacements for sold-out hardware: an
	// approval covers free hardware with at least as much memory costing up
	// to this many times the approved price. 1 turns replacements off.
	FallbackPriceFactor float64 `json:"fallback_price_factor,omitempty"`
}

// fallbackProvider can suggest replacements for sold-out hardware.
type fallbackProvider interface {
	alternatives(name string, factor float64) ([]computeHardware, error)
}

func openServer(dir string, log io.Writer) (*hiServer, error) {
	server := &hiServer{
		dir:       dir,
		keys:      map[string]string{},
		providers: map[string]computeProvider{},
		nonces:    map[string]time.Time{},
		unleased:  map[string]bool{},
		log:       log,

		fallbackFactor: serverFallbackFactor,
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
func (s *hiServer) verifyRequest(request *http.Request) (ed25519.PublicKey, []byte, error) {
	body, err := io.ReadAll(io.LimitReader(request.Body, serverMaxBody))
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
}

type apiEnroll struct {
	User     string `json:"user"`
	Hostname string `json:"hostname"`
}

type apiComputeRequest struct {
	Provider   string `json:"provider"`
	Hardware   string `json:"hardware"`
	Name       string `json:"name"`
	MaxSeconds int64  `json:"max_seconds"`
	Image      string `json:"image,omitempty"`
	PublicKey  string `json:"public_key"`
	Reason     string `json:"reason"`
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
	return mux
}

type deviceHandler func(w http.ResponseWriter, r *http.Request, device serverDevice, body []byte)

func (s *hiServer) device(next deviceHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		public, body, err := s.verifyRequest(r)
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
		copy := *device
		s.mu.Unlock()
		next(w, r, copy, body)
	}
}

func (s *hiServer) handleEnroll(w http.ResponseWriter, r *http.Request) {
	public, body, err := s.verifyRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, err.Error())
		return
	}
	var enroll apiEnroll
	if err := json.Unmarshal(body, &enroll); err != nil || !validServerName(enroll.User) || len(enroll.Hostname) > 64 {
		writeAPIError(w, http.StatusBadRequest, "use a user name of lowercase letters, digits, dots, or hyphens")
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
	s.state.Requests[request.ID] = request
	if err := s.saveLocked(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(enroll.User, "requested enrollment", request.ID, fmt.Sprintf("%s from %s", fingerprint, enroll.Hostname))
	writeJSON(w, http.StatusAccepted, request)
}

func (s *hiServer) handleMe(w http.ResponseWriter, _ *http.Request, device serverDevice, _ []byte) {
	s.mu.Lock()
	user := s.state.Users[device.User]
	me := apiMe{User: user.Name, Group: user.Group, Device: device.Fingerprint}
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
	options, err := provider.hardware()
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

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.state.Leases[input.Name]; taken || s.nameRequestedLocked(input.Name) {
		writeAPIError(w, http.StatusConflict, fmt.Sprintf("an instance named %q already exists; choose another --name", input.Name))
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
	}
	s.state.Requests[request.ID] = request
	if err := s.saveLocked(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(device.User, "requested compute", request.ID, describeServerRequest(request))
	writeJSON(w, http.StatusAccepted, request)
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
	writeJSON(w, http.StatusOK, apiSSH{Options: target.options, Destination: target.destination,
		Hint: "hi: the instance accepts the SSH key from ~/.ssh that hi sent with the request"})
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
		return copy, err
	}
	if request.Kind == "enroll" {
		user, exists := s.state.Users[request.User]
		if !exists {
			user = &serverUser{Name: request.User, Group: group, Added: computeNow()}
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
		return copy, err
	}
	request.State = "starting"
	err := s.saveLocked()
	copy := *request
	s.mu.Unlock()
	s.audit(actor, "approved", id, describeServerRequest(&copy))
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
		if request, ok := p.server.state.Requests[p.id]; ok {
			request.Progress = last
		}
		p.server.mu.Unlock()
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
	// memory, up to fallbackFactor times the approved price.
	var soldOut noCapacityError
	if err != nil && errors.As(err, &soldOut) && s.fallbackFactor > 1 {
		if fallback, ok := provider.(fallbackProvider); ok {
			alternatives, listErr := fallback.alternatives(hardware.name, s.fallbackFactor)
			if listErr == nil && len(alternatives) == 0 {
				err = fmt.Errorf("%s is sold out, and nothing free with as much memory costs at most %gx its %s",
					hardware.name, s.fallbackFactor, hardware.rate)
			}
			for i, alternative := range alternatives {
				if i == 3 {
					break
				}
				fmt.Fprintf(progress, "%s is sold out; starting %s (%s) instead\n", hardware.name, alternative.name, alternative.rate)
				s.audit("server", "replaced sold-out hardware", request.ID, fmt.Sprintf("%s (%s) with %s (%s), within %gx",
					hardware.name, hardware.rate, alternative.name, alternative.rate, s.fallbackFactor))
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
		current.State, current.Progress = "running", ""
	}
	s.saveLocked()
	s.mu.Unlock()
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
	s.audit(actor, "stopped", lease.Name, fmt.Sprintf("ran %s, started by %s",
		formatDuration(computeNow().Sub(lease.Started)), lease.User))
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
		request.State, request.StoppedBy = "stopped", stoppedBy
	}
	s.saveLocked()
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
				}
			}
		}
		for _, instance := range instances {
			key := name + "/" + instance.name
			if leased[instance.name] || starting[instance.name] || s.unleased[key] {
				continue
			}
			s.unleased[key] = true
			s.audit("server", "unleased instance", key, "running on the organization's account without a request")
		}
	}
}

// ---------------------------------------------------------------------------
// admin API, over a Unix socket that only the server's user can open

func (s *hiServer) adminHandler() http.Handler {
	mux := http.NewServeMux()
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
			raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(input.Key))
			if err != nil || len(raw) != ed25519.PublicKeySize {
				writeAPIError(w, http.StatusBadRequest, "that is not a device key; get it with `hi connect key` on the device")
				return
			}
			public = raw
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
	return mux
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

	ticker := time.NewTicker(serverReconcileEvery)
	defer ticker.Stop()
	s.reconcile()
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
	default:
		fmt.Fprintf(stderr, "hi: unknown server command %q\n\n", command)
		printServerUsage(stderr)
		return 2
	}
}

func printServerUsage(w io.Writer) {
	fmt.Fprintln(w, `hi server brokers compute for connected devices: it holds the provider keys,
and nothing starts without an approval.

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
		server.fallbackFactor = config.FallbackPriceFactor
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
