package main

import (
	"archive/tar"
	"bytes"
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
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing/fstest"
	"time"
)

// Template sources: private repositories of templates and skills that the
// server mirrors and serves, signed, to enrolled devices
// (docs/specs/approved/hi_server.md#template-sources).

var templateSyncEvery = 15 * time.Minute

// templateGitTimeout bounds every git command, so a stuck connection or
// credential helper can't hold the template lock and block the admin
// commands behind it.
var templateGitTimeout = 2 * time.Minute

const (
	// templateBundleLimit bounds a source's archive; templates are text.
	templateBundleLimit = 4 << 20
	templateTokenPrefix = "template:"
	serverKeyFile       = "server_key"
)

// templateSource is what the server knows about one source. Its token is
// kept in keys.json, never here.
type templateSource struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Ref  string `json:"ref,omitempty"`
	// Commit is the commit devices get now; Commits is every commit the
	// server has served, so a repository's recorded commit stays available.
	Commit  string             `json:"commit"`
	Commits []string           `json:"commits"`
	Layers  []apiTemplateLayer `json:"layers"`
	Skills  []string           `json:"skills"`
	Fetched time.Time          `json:"fetched,omitzero"`
	// Problem is why the newest commit on the ref is not served, if it isn't.
	Problem string `json:"problem,omitempty"`
}

type apiTemplateLayer struct {
	Name       string `json:"name"`
	Summary    string `json:"summary,omitempty"`
	Extends    string `json:"extends,omitempty"`
	Hidden     bool   `json:"hidden,omitempty"`
	RequiresHi string `json:"requires_hi,omitempty"`
}

type apiTemplateCatalog struct {
	Sources []apiTemplateSourceInfo `json:"sources"`
}

type apiTemplateSourceInfo struct {
	Name   string             `json:"name"`
	Commit string             `json:"commit"`
	Layers []apiTemplateLayer `json:"layers"`
	Skills []string           `json:"skills"`
}

// apiTemplateBundle is a source at one commit, as a tar.gz archive signed
// with the server key.
type apiTemplateBundle struct {
	Source    string `json:"source"`
	Commit    string `json:"commit"`
	Archive   []byte `json:"archive"`
	Signature []byte `json:"signature"`
}

func templateBundlePayload(source, commit string, archive []byte) []byte {
	sum := sha256.Sum256(archive)
	return []byte(strings.Join([]string{"hi template bundle v1", source, commit, hex.EncodeToString(sum[:])}, "\n"))
}

// ---------------------------------------------------------------------------
// the server key

// loadServerKey reads the server's signing key, creating it on first use.
func loadServerKey(dir string) (ed25519.PrivateKey, error) {
	path := filepath.Join(dir, serverKeyFile)
	data, err := os.ReadFile(path)
	if err == nil {
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("%s is not a hi server key", path)
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(key.Seed()) + "\n"
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// ---------------------------------------------------------------------------
// mirroring and checking

func (s *hiServer) templateMirror(name string) string {
	return filepath.Join(s.dir, "templates", name+".git")
}

// git runs git without prompts. A token goes in through the environment,
// never in arguments or the stored remote URL, and is removed from errors.
func runTemplateGit(token string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), templateGitTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "git", args...)
	command.WaitDelay = 5 * time.Second
	// A transfer slower than 1 KB/s for 30 seconds is given up on, well
	// before the overall timeout.
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true", "SSH_ASKPASS=true",
		"GIT_HTTP_LOW_SPEED_LIMIT=1000", "GIT_HTTP_LOW_SPEED_TIME=30")
	if token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		command.Env = append(command.Env, "GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic "+basic)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("git %s: no answer within %s", args[0], templateGitTimeout)
	}
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if token != "" {
			message = strings.ReplaceAll(message, token, "***")
		}
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", args[0], message)
	}
	return output, nil
}

func validTemplateURL(url string) bool {
	for _, prefix := range []string{"https://", "ssh://", "git@", "file://", "/"} {
		if strings.HasPrefix(url, prefix) {
			return true
		}
	}
	return false
}

// readTemplateCommit reads a commit of a mirror into memory.
func readTemplateCommit(mirror, commit string) (fs.FS, error) {
	data, err := runTemplateGit("", "-C", mirror, "archive", "--format=tar", commit)
	if err != nil {
		return nil, err
	}
	if len(data) > 4*templateBundleLimit {
		return nil, fmt.Errorf("the repository is %d MB; a template source must stay under %d MB", len(data)>>20, 4*templateBundleLimit>>20)
	}
	files := fstest.MapFS{}
	reader := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		files[path.Clean(header.Name)] = &fstest.MapFile{Data: content, Mode: 0o644}
	}
}

// checkTemplateSource checks what a source holds at a commit: valid layer
// manifests, no built-in names, parents that exist, and skills with a
// SKILL.md.
func checkTemplateSource(name string, files fs.FS) ([]apiTemplateLayer, []string, error) {
	layers, err := loadTemplateSource(name, files)
	if err != nil {
		return nil, nil, err
	}
	builtin, err := builtinTemplateLayers()
	if err != nil {
		return nil, nil, err
	}
	var skills []string
	if entries, err := fs.ReadDir(files, "skills"); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if _, err := fs.Stat(files, path.Join("skills", entry.Name(), "SKILL.md")); err != nil {
				return nil, nil, fmt.Errorf("skills/%s has no SKILL.md", entry.Name())
			}
			skills = append(skills, entry.Name())
		}
	}
	if len(layers) == 0 && len(skills) == 0 {
		return nil, nil, errors.New("no layers (<layer>/layer.json) and no skills (skills/<name>/SKILL.md) found")
	}
	combined := map[string]*templateLayer{}
	for layerName, layer := range builtin {
		combined[layerName] = layer
	}
	var problems []string
	for layerName, layer := range layers {
		if _, taken := builtin[layerName]; taken {
			problems = append(problems, fmt.Sprintf("%s is a built-in template name", layerName))
		}
		combined[layerName] = layer
	}
	var listed []apiTemplateLayer
	for layerName, layer := range layers {
		if _, err := layerChain(combined, layerName); err != nil {
			problems = append(problems, err.Error())
		}
		for _, skill := range layer.Skills {
			if skill != "hi" && !containsString(skills, skill) {
				problems = append(problems, fmt.Sprintf("%s names the skill %q, which is not in skills/", layerName, skill))
			}
		}
		listed = append(listed, apiTemplateLayer{Name: layerName, Summary: layer.Summary, Extends: layer.Extends,
			Hidden: layer.Hidden, RequiresHi: layer.RequiresHi})
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, nil, errors.New(strings.Join(problems, "; "))
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].Name < listed[j].Name })
	sort.Strings(skills)
	return listed, skills, nil
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// addTemplateSource mirrors a repository and serves it once its current
// commit passes the checks.
func (s *hiServer) addTemplateSource(name, url, ref, token, actor string) (templateSource, error) {
	s.templateMu.Lock()
	defer s.templateMu.Unlock()
	if !validServerName(name) || name == "builtin" || name == "local" {
		return templateSource{}, serverUsageError{fmt.Sprintf("invalid source name %q; use lowercase letters, digits, and dashes, not builtin or local", name)}
	}
	if !validTemplateURL(url) {
		return templateSource{}, serverUsageError{"use an https://, ssh://, or git@ URL"}
	}
	if strings.HasPrefix(ref, "-") {
		return templateSource{}, serverUsageError{fmt.Sprintf("invalid ref %q", ref)}
	}
	s.mu.Lock()
	_, exists := s.state.TemplateSources[name]
	s.mu.Unlock()
	if exists {
		return templateSource{}, serverUsageError{fmt.Sprintf("there is already a source named %s; remove it first", name)}
	}

	mirror := s.templateMirror(name)
	if err := os.MkdirAll(filepath.Dir(mirror), 0o700); err != nil {
		return templateSource{}, err
	}
	os.RemoveAll(mirror)
	if _, err := runTemplateGit(token, "clone", "--quiet", "--mirror", "--", url, mirror); err != nil {
		return templateSource{}, err
	}
	commit, err := resolveTemplateRef(mirror, ref)
	if err == nil {
		var files fs.FS
		if files, err = readTemplateCommit(mirror, commit); err == nil {
			var source templateSource
			source.Layers, source.Skills, err = checkTemplateSource(name, files)
			if err == nil {
				source.Name, source.URL, source.Ref = name, url, ref
				source.Commit, source.Commits, source.Fetched = commit, []string{commit}, computeNow()
				s.mu.Lock()
				s.state.TemplateSources[name] = &source
				if token != "" {
					s.keys[templateTokenPrefix+name] = token
				}
				err = errors.Join(s.saveLocked(), s.saveKeysLocked())
				s.mu.Unlock()
				if err != nil {
					return templateSource{}, err
				}
				s.audit(actor, "added template source", name, fmt.Sprintf("%s at %s: %s", url, shortCommit(commit), describeTemplateContents(source)))
				return source, nil
			}
		}
	}
	os.RemoveAll(mirror)
	return templateSource{}, fmt.Errorf("%s was not added: %w", name, err)
}

func resolveTemplateRef(mirror, ref string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	output, err := runTemplateGit("", "-C", mirror, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("the repository has no %s", ref)
	}
	return strings.TrimSpace(string(output)), nil
}

// renameTemplateSource changes a source's name and keeps its mirror, token,
// and commits. Devices fetch it again under the new name.
func (s *hiServer) renameTemplateSource(name, to, actor string) error {
	s.templateMu.Lock()
	defer s.templateMu.Unlock()
	if !validServerName(to) || to == "builtin" || to == "local" {
		return serverUsageError{fmt.Sprintf("invalid source name %q; use lowercase letters, digits, and dashes, not builtin or local", to)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	source, ok := s.state.TemplateSources[name]
	if !ok {
		return serverUsageError{fmt.Sprintf("no template source named %q", name)}
	}
	if _, taken := s.state.TemplateSources[to]; taken {
		return serverUsageError{fmt.Sprintf("there is already a source named %s", to)}
	}
	if err := os.Rename(s.templateMirror(name), s.templateMirror(to)); err != nil {
		return err
	}
	delete(s.state.TemplateSources, name)
	source.Name = to
	s.state.TemplateSources[to] = source
	if token, ok := s.keys[templateTokenPrefix+name]; ok {
		delete(s.keys, templateTokenPrefix+name)
		s.keys[templateTokenPrefix+to] = token
	}
	if err := errors.Join(s.saveLocked(), s.saveKeysLocked()); err != nil {
		return err
	}
	s.audit(actor, "renamed template source", name, "to "+to)
	return nil
}

func (s *hiServer) removeTemplateSource(name, actor string) error {
	s.templateMu.Lock()
	defer s.templateMu.Unlock()
	s.mu.Lock()
	if _, ok := s.state.TemplateSources[name]; !ok {
		s.mu.Unlock()
		return serverUsageError{fmt.Sprintf("no template source named %q", name)}
	}
	delete(s.state.TemplateSources, name)
	delete(s.keys, templateTokenPrefix+name)
	err := errors.Join(s.saveLocked(), s.saveKeysLocked())
	s.mu.Unlock()
	if err != nil {
		return err
	}
	os.RemoveAll(s.templateMirror(name))
	s.audit(actor, "removed template source", name, "")
	return nil
}

// syncTemplateSources fetches every source, or the named one, and serves
// a new commit only when it passes the checks.
func (s *hiServer) syncTemplateSources(only, actor string) ([]templateSource, error) {
	s.templateMu.Lock()
	defer s.templateMu.Unlock()
	s.mu.Lock()
	var names []string
	for name := range s.state.TemplateSources {
		if only == "" || name == only {
			names = append(names, name)
		}
	}
	s.mu.Unlock()
	if only != "" && len(names) == 0 {
		return nil, serverUsageError{fmt.Sprintf("no template source named %q", only)}
	}
	sort.Strings(names)
	var results []templateSource
	for _, name := range names {
		results = append(results, s.syncTemplateSource(name, actor))
	}
	return results, nil
}

func (s *hiServer) syncTemplateSource(name, actor string) templateSource {
	s.mu.Lock()
	source := *s.state.TemplateSources[name]
	token := s.keys[templateTokenPrefix+name]
	s.mu.Unlock()
	mirror := s.templateMirror(name)

	commit, problem := "", ""
	if _, err := runTemplateGit(token, "-C", mirror, "fetch", "--quiet", "--prune", "origin"); err != nil {
		problem = err.Error()
	} else if commit, err = resolveTemplateRef(mirror, source.Ref); err != nil {
		problem = err.Error()
	} else if commit != source.Commit {
		files, err := readTemplateCommit(mirror, commit)
		var layers []apiTemplateLayer
		var skills []string
		if err == nil {
			layers, skills, err = checkTemplateSource(name, files)
		}
		if err != nil {
			problem = fmt.Sprintf("%s is not served: %v", shortCommit(commit), err)
		} else {
			previous := source.Commit
			source.Commit, source.Layers, source.Skills = commit, layers, skills
			if !containsString(source.Commits, commit) {
				source.Commits = append(source.Commits, commit)
			}
			s.audit(actor, "updated template source", name, fmt.Sprintf("%s to %s: %s",
				shortCommit(previous), shortCommit(commit), describeTemplateContents(source)))
		}
	}

	s.mu.Lock()
	stored := s.state.TemplateSources[name]
	if stored == nil { // removed meanwhile
		s.mu.Unlock()
		return source
	}
	source.Fetched = computeNow()
	source.Problem = problem
	*stored = source
	alertKey := "templates:" + name + ":" + problem
	alert := problem != "" && !s.state.Alerts[alertKey]
	if alert {
		if s.state.Alerts == nil {
			s.state.Alerts = map[string]bool{}
		}
		s.state.Alerts[alertKey] = true
	}
	s.saveLocked()
	s.mu.Unlock()
	if alert {
		s.audit(actor, "template source problem", name, problem)
		s.notifyChannel(fmt.Sprintf("⚠️ Template source %s: %s. Devices keep getting %s.", name, problem, shortCommit(source.Commit)))
	}
	return source
}

func shortCommit(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

func describeTemplateContents(source templateSource) string {
	var layers []string
	for _, layer := range source.Layers {
		layers = append(layers, layer.Name)
	}
	return fmt.Sprintf("layers %s; skills %s", listOrNone(layers), listOrNone(source.Skills))
}

func listOrNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

// syncTemplatesInBackground keeps sources current while the server runs.
func (s *hiServer) syncTemplatesInBackground() {
	s.mu.Lock()
	any := len(s.state.TemplateSources) > 0
	s.mu.Unlock()
	if any && s.templateMu.TryLock() {
		s.templateMu.Unlock()
		go s.syncTemplateSources("", "server")
	}
}

// ---------------------------------------------------------------------------
// serving devices

// templateSourcesFor returns the sources a user's group may use: every
// source unless policy lists them.
func (s *hiServer) templateSourcesFor(user string) []templateSource {
	policy := s.policy()
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.state.Users[user]
	if record == nil || record.Kind == "viewer" {
		return nil
	}
	var allowed *[]string
	if group, ok := policy.Groups[record.Group]; ok {
		allowed = group.TemplateSources
	}
	var sources []templateSource
	for name, source := range s.state.TemplateSources {
		if source.Commit == "" || (allowed != nil && !containsString(*allowed, name)) {
			continue
		}
		sources = append(sources, *source)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	return sources
}

func (s *hiServer) handleTemplateCatalog(w http.ResponseWriter, _ *http.Request, device serverDevice, _ []byte) {
	catalog := apiTemplateCatalog{Sources: []apiTemplateSourceInfo{}}
	for _, source := range s.templateSourcesFor(device.User) {
		catalog.Sources = append(catalog.Sources, apiTemplateSourceInfo{
			Name: source.Name, Commit: source.Commit, Layers: source.Layers, Skills: source.Skills})
	}
	writeJSON(w, http.StatusOK, catalog)
}

func (s *hiServer) handleTemplateBundle(w http.ResponseWriter, r *http.Request, device serverDevice, _ []byte) {
	name, commit := r.PathValue("source"), r.PathValue("commit")
	var source *templateSource
	for _, candidate := range s.templateSourcesFor(device.User) {
		if candidate.Name == name {
			source = &candidate
		}
	}
	if source == nil {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("no template source %q for you", name))
		return
	}
	if !containsString(source.Commits, commit) {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("%s has never served %s", name, shortCommit(commit)))
		return
	}
	archive, err := runTemplateGit("", "-C", s.templateMirror(name), "archive", "--format=tar.gz", commit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(archive) > templateBundleLimit {
		writeAPIError(w, http.StatusInternalServerError, "the template source is too large to serve")
		return
	}
	s.mu.Lock()
	key := s.serverKey
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, apiTemplateBundle{Source: name, Commit: commit, Archive: archive,
		Signature: ed25519.Sign(key, templateBundlePayload(name, commit, archive))})
}

// ---------------------------------------------------------------------------
// admin API and commands

func (s *hiServer) templateAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/templates", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.templateSourceList())
	})
	mux.HandleFunc("POST /admin/templates", func(w http.ResponseWriter, r *http.Request) {
		var input struct{ As, Name, URL, Ref, Token string }
		json.NewDecoder(io.LimitReader(r.Body, serverMaxBody)).Decode(&input)
		source, err := s.addTemplateSource(input.Name, input.URL, input.Ref, input.Token, input.As)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, source)
	})
	mux.HandleFunc("DELETE /admin/templates/{name}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.removeTemplateSource(r.PathValue("name"), r.URL.Query().Get("as")); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /admin/templates/{name}/rename", func(w http.ResponseWriter, r *http.Request) {
		var input struct{ As, To string }
		json.NewDecoder(io.LimitReader(r.Body, serverMaxBody)).Decode(&input)
		if err := s.renameTemplateSource(r.PathValue("name"), input.To, input.As); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /admin/templates/sync", func(w http.ResponseWriter, r *http.Request) {
		var input struct{ As, Name string }
		json.NewDecoder(io.LimitReader(r.Body, serverMaxBody)).Decode(&input)
		results, err := s.syncTemplateSources(input.Name, input.As)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, results)
	})
}

func (s *hiServer) templateSourceList() []templateSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := []templateSource{}
	for _, source := range s.state.TemplateSources {
		list = append(list, *source)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

func serverTemplatesCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("templates", stderr)
	as := flags.String("as", currentUserName(), "who is acting")
	ref := flags.String("ref", "", "branch or tag to serve (default: the repository's default branch)")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	usage := usageError{"usage: hi server templates add <name> <git-url> [--ref <ref>] | rename <name> <new-name> | remove <name> | list | sync [<name>]"}
	if len(positional) == 0 {
		return usage
	}
	dir, err := serverDirectory(*dirFlag)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		return fmt.Errorf("no server in %s; run `hi server init` first", dir)
	}
	running := false
	if _, err := os.Stat(filepath.Join(dir, "admin.sock")); err == nil {
		running = true
	}
	// Without a running server, work on its state directly; it reads the
	// result when it starts.
	direct := func() (*hiServer, error) { return openServer(dir, io.Discard) }

	switch {
	case positional[0] == "list" && len(positional) == 1:
		var sources []templateSource
		if running {
			err = adminCall(*dirFlag, http.MethodGet, "/admin/templates", nil, &sources)
		} else {
			var server *hiServer
			if server, err = direct(); err == nil {
				sources = server.templateSourceList()
			}
		}
		if err != nil {
			return err
		}
		printTemplateSources(sources, stdout)
		return nil

	case positional[0] == "add" && len(positional) == 3:
		name, url := positional[1], positional[2]
		token := ""
		if strings.HasPrefix(url, "https://") {
			if token, err = readTemplateToken(name, stdin, stdout); err != nil {
				return err
			}
		}
		fmt.Fprintf(stdout, "Mirroring %s and checking it…\n", url)
		var source templateSource
		if running {
			body := map[string]string{"as": *as, "name": name, "url": url, "ref": *ref, "token": token}
			err = adminCall(*dirFlag, http.MethodPost, "/admin/templates", body, &source)
		} else {
			var server *hiServer
			if server, err = direct(); err == nil {
				source, err = server.addTemplateSource(name, url, *ref, token, *as)
			}
		}
		if err != nil {
			return err
		}
		printTemplateSources([]templateSource{source}, stdout)
		fmt.Fprintf(stdout, "Connected devices now see these in `hi init --list`.\n")
		return nil

	case positional[0] == "rename" && len(positional) == 3:
		if running {
			err = adminCall(*dirFlag, http.MethodPost, "/admin/templates/"+positional[1]+"/rename", map[string]string{"as": *as, "to": positional[2]}, nil)
		} else {
			var server *hiServer
			if server, err = direct(); err == nil {
				err = server.renameTemplateSource(positional[1], positional[2], *as)
			}
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Renamed %s to %s. Devices see the new name the next time they run hi init.\n", positional[1], positional[2])
		return nil

	case positional[0] == "remove" && len(positional) == 2:
		if running {
			err = adminCall(*dirFlag, http.MethodDelete, "/admin/templates/"+positional[1]+"?as="+*as, nil, nil)
		} else {
			var server *hiServer
			if server, err = direct(); err == nil {
				err = server.removeTemplateSource(positional[1], *as)
			}
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Removed %s. Repositories made from it keep their files.\n", positional[1])
		return nil

	case positional[0] == "sync" && len(positional) <= 2:
		name := ""
		if len(positional) == 2 {
			name = positional[1]
		}
		var sources []templateSource
		if running {
			err = adminCall(*dirFlag, http.MethodPost, "/admin/templates/sync", map[string]string{"as": *as, "name": name}, &sources)
		} else {
			var server *hiServer
			if server, err = direct(); err == nil {
				sources, err = server.syncTemplateSources(name, *as)
			}
		}
		if err != nil {
			return err
		}
		printTemplateSources(sources, stdout)
		return nil
	}
	return usage
}

func printTemplateSources(sources []templateSource, stdout io.Writer) {
	if len(sources) == 0 {
		fmt.Fprintln(stdout, "No template sources; add one with `hi server templates add <name> <git-url>`.")
		return
	}
	for _, source := range sources {
		ref := source.Ref
		if ref == "" {
			ref = "default branch"
		}
		fmt.Fprintf(stdout, "%s  %s (%s) at %s\n", source.Name, source.URL, ref, shortCommit(source.Commit))
		fmt.Fprintf(stdout, "  %s\n", describeTemplateContents(source))
		if !source.Fetched.IsZero() {
			fmt.Fprintf(stdout, "  fetched %s\n", source.Fetched.Local().Format("2006-01-02 15:04"))
		}
		if source.Problem != "" {
			fmt.Fprintf(stdout, "  problem: %s\n", source.Problem)
		}
	}
}

// readTemplateToken asks for a read-only token without echo. An empty
// answer means a repository that needs none.
func readTemplateToken(name string, stdin io.Reader, stdout io.Writer) (string, error) {
	if file, ok := stdin.(*os.File); ok && isTerminal(stdin) {
		fmt.Fprintf(stdout, "Read-only token for %s (a fine-grained token with contents read access to this one repository;\n", name)
		fmt.Fprint(stdout, "empty for a public repository; it shows as *): ")
		token, err := readSecret(file, stdout)
		fmt.Fprintln(stdout)
		return strings.TrimSpace(string(token)), err
	}
	line, err := readLine(stdin)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
