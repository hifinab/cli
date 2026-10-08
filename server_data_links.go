package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// Signed links for compute instances. A rented instance can't reach the hi
// server, which listens only inside NetBird, so the device asks the server
// for a link per file instead: the server resolves each file with the
// organization's token, and the Hub answers with a signed CDN link that
// needs no token and lasts about an hour. Small files kept in git have no
// CDN link; the server sends their contents instead.

const (
	dataLinksMaxFiles   = 2000
	dataLinksMaxInline  = 10 << 20 // one small file sent as contents
	dataLinksInlineSum  = 32 << 20 // all of them
	dataLinksWorkers    = 8
	dataLinksTreePages  = 50
	dataLinksExpiryNote = "about an hour"
)

// apiDataLinksRequest is POST /v1/data/links.
type apiDataLinksRequest struct {
	Scope    string   `json:"scope"` // <kind>:<org>/<name>
	Revision string   `json:"revision,omitempty"`
	Include  []string `json:"include,omitempty"`
}

type apiDataLink struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	URL     string `json:"url,omitempty"`
	Content []byte `json:"content,omitempty"` // a small file kept in git
}

type apiDataLinks struct {
	Kind     string        `json:"kind"`
	ID       string        `json:"id"`
	Revision string        `json:"revision,omitempty"`
	Files    []apiDataLink `json:"files"`
	Expires  string        `json:"expires"`
}

// dataGlob matches like hf's --include: * and ? match across folders.
func dataGlob(pattern string) *regexp.Regexp {
	var expression strings.Builder
	expression.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			expression.WriteString(".*")
		case '?':
			expression.WriteString(".")
		default:
			expression.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	expression.WriteString("$")
	return regexp.MustCompile(expression.String())
}

func dataMatchesAny(patterns []*regexp.Regexp, path string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}

// hubTree lists every file of a repository at a commit, or of a bucket.
func hubTree(token, kind, id, commit string) ([]apiDataLink, error) {
	next := "/api/" + kind + "s/" + id + "/tree/" + url.PathEscape(commit) + "?recursive=true"
	if kind == "bucket" {
		next = "/api/buckets/" + id + "/tree?recursive=true"
	}
	var files []apiDataLink
	for page := 0; next != ""; page++ {
		if page == dataLinksTreePages {
			return nil, fmt.Errorf("%s has more files than hi lists for one run; name a folder or pattern", id)
		}
		var entries []struct {
			Type string `json:"type"`
			Path string `json:"path"`
			Size int64  `json:"size"`
		}
		var err error
		if next, err = hubGet(token, next, &entries); err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Type == "file" {
				files = append(files, apiDataLink{Path: entry.Path, Size: entry.Size})
			}
		}
	}
	return files, nil
}

func escapeDataPath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// hubLink resolves one file: a signed link when the Hub redirects off the
// Hub, the contents otherwise.
func hubLink(token, kind, id, commit string, file *apiDataLink) error {
	resolve := map[string]string{
		"dataset": "/datasets/" + id + "/resolve/" + url.PathEscape(commit) + "/",
		"model":   "/" + id + "/resolve/" + url.PathEscape(commit) + "/",
		"bucket":  "/buckets/" + id + "/resolve/",
	}[kind] + escapeDataPath(file.Path)
	request, err := http.NewRequest(http.MethodHead, dataHubURL+resolve, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "hi/"+version)
	client := &http.Client{Timeout: hubClient.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("can't reach Hugging Face: %w", err)
	}
	response.Body.Close()
	location := response.Header.Get("Location")
	hub := strings.TrimRight(dataHubURL, "/")
	if response.StatusCode >= 300 && response.StatusCode < 400 && strings.HasPrefix(location, "http") && !strings.HasPrefix(location, hub+"/") {
		file.URL = location
		return nil
	}
	if response.StatusCode >= 400 {
		return fmt.Errorf("Hugging Face answered %s for %s", response.Status, file.Path)
	}
	if file.Size > dataLinksMaxInline {
		return fmt.Errorf("%s has no download link and is too large to send", file.Path)
	}
	// A small file kept in git: fetch it, following redirects within the Hub.
	request, _ = http.NewRequest(http.MethodGet, dataHubURL+resolve, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "hi/"+version)
	response, err = hubClient.Do(request)
	if err != nil {
		return fmt.Errorf("can't reach Hugging Face: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Hugging Face answered %s for %s", response.Status, file.Path)
	}
	file.Content, err = io.ReadAll(io.LimitReader(response.Body, dataLinksMaxInline+1))
	if err == nil && len(file.Content) > dataLinksMaxInline {
		err = fmt.Errorf("%s is too large to send", file.Path)
	}
	return err
}

func (s *hiServer) handleDataLinks(w http.ResponseWriter, _ *http.Request, device serverDevice, body []byte) {
	group, ok := s.userGroup(device.User)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "a dashboard viewer can't use hi data")
		return
	}
	var input apiDataLinksRequest
	if err := json.Unmarshal(body, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "the request is not JSON")
		return
	}
	kind, id, err := parseDataScope(input.Scope)
	if err == nil && id == "" {
		err = errors.New("name one repository or bucket")
	}
	if err == nil && kind == "bucket" && input.Revision != "" {
		err = errors.New("buckets have no revisions")
	}
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	org, _, _ := strings.Cut(id, "/")
	token, ok := s.dataOrgs()[org]
	if !ok {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("this server doesn't serve the %s organization", org))
		return
	}
	if !dataAllowed(s.policy(), group, id) {
		writeAPIError(w, http.StatusForbidden, fmt.Sprintf("your group %s may not read %s", group, id))
		return
	}

	links := apiDataLinks{Kind: kind, ID: id, Expires: dataLinksExpiryNote}
	if kind != "bucket" {
		path := "/api/" + kind + "s/" + id
		if input.Revision != "" {
			path += "/revision/" + url.PathEscape(input.Revision)
		}
		var info struct {
			SHA string `json:"sha"`
		}
		if _, err := hubGet(token, path, &info); err != nil || info.SHA == "" {
			writeAPIError(w, http.StatusBadGateway, fmt.Sprintf("can't find %s of %s: %v", firstNonEmpty(input.Revision, "the default branch"), id, err))
			return
		}
		links.Revision = info.SHA
	}
	files, err := hubTree(token, kind, id, links.Revision)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error())
		return
	}
	var patterns []*regexp.Regexp
	for _, pattern := range input.Include {
		patterns = append(patterns, dataGlob(pattern))
	}
	for _, file := range files {
		if dataMatchesAny(patterns, file.Path) {
			links.Files = append(links.Files, file)
		}
	}
	switch {
	case len(links.Files) == 0:
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("no files in %s match %s", id, strings.Join(input.Include, ", ")))
		return
	case len(links.Files) > dataLinksMaxFiles:
		writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("%s has %d files; one run takes up to %d, so name a folder or pattern", id, len(links.Files), dataLinksMaxFiles))
		return
	}

	// Resolve the files a few at a time; the Hub limits calls per token.
	var wait sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	queue := make(chan int)
	for range dataLinksWorkers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for i := range queue {
				if err := hubLink(token, kind, id, links.Revision, &links.Files[i]); err != nil {
					mu.Lock()
					firstErr = cmpOr(firstErr, err)
					mu.Unlock()
				}
			}
		}()
	}
	for i := range links.Files {
		queue <- i
	}
	close(queue)
	wait.Wait()
	if firstErr != nil {
		writeAPIError(w, http.StatusBadGateway, firstErr.Error())
		return
	}
	inline := 0
	for _, file := range links.Files {
		inline += len(file.Content)
	}
	if inline > dataLinksInlineSum {
		writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("%s has too many files without download links to send; name a folder or pattern", id))
		return
	}
	at := ""
	if links.Revision != "" {
		at = " at " + shortCommit(links.Revision)
	}
	s.audit(device.User, "data links", input.Scope, fmt.Sprintf("%s%s, for a compute run from %s", plural(len(links.Files), "file"), at, device.Hostname))
	writeJSON(w, http.StatusOK, links)
}

func cmpOr(first, next error) error {
	if first != nil {
		return first
	}
	return next
}
