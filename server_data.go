package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// hi data: the server holds a Hugging Face token per organization and
// runs a proxy for the Hub's read calls, so devices use the official hf
// tools with HF_ENDPOINT pointing here and never hold a Hugging Face token
// (docs/specs/approved/hi_data.md).

const (
	dataKeyPrefix   = "data:" // in keys.json: data:<org>
	dataTokenPrefix = "hidata_"
	// dataTokenLifetime covers long downloads: hf asks the proxy for a new
	// Xet token every 15 minutes with the same hi data token. A token works
	// only for its device, and only while the device and user are enrolled.
	dataTokenLifetime = 24 * time.Hour
	dataListEvery     = 5 * time.Minute
	dataProxyPath     = "/hf"
	dataListPages     = 20
)

// dataHubURL is the Hugging Face Hub; tests point it at a fake.
var dataHubURL = "https://huggingface.co"

var dataKinds = []string{"dataset", "model", "bucket"}

// dataItem is one dataset, model, or bucket a device may download.
type dataItem struct {
	Kind    string    `json:"kind"`
	ID      string    `json:"id"`
	Private bool      `json:"private,omitempty"`
	Size    int64     `json:"size,omitempty"`
	Updated time.Time `json:"updated,omitzero"`
	// Commit is a dataset's or model's current commit; buckets have none.
	Commit string `json:"commit,omitempty"`
	// Files is a bucket's file count.
	Files int `json:"files,omitempty"`
}

// dataListing is an organization's datasets and models, as last fetched.
type dataListing struct {
	fetched time.Time
	items   []dataItem
	problem string
}

type apiDataOrg struct {
	Name     string `json:"name"`
	Datasets int    `json:"datasets"`
	Models   int    `json:"models"`
	Buckets  int    `json:"buckets"`
	Problem  string `json:"problem,omitempty"`
}

// apiDataCatalog is GET /v1/data.
type apiDataCatalog struct {
	Orgs  []apiDataOrg `json:"orgs"`
	Items []dataItem   `json:"items"`
}

// apiDataToken is POST /v1/data/token's answer. Path is appended to the
// server's URL to make HF_ENDPOINT.
type apiDataToken struct {
	Token   string    `json:"token"`
	Expires time.Time `json:"expires"`
	Path    string    `json:"path"`
}

// apiDataOrgStatus is what the admin commands show for an organization.
type apiDataOrgStatus struct {
	Org      string `json:"org"`
	Account  string `json:"account,omitempty"`
	Role     string `json:"role,omitempty"`
	Datasets int    `json:"datasets"`
	Models   int    `json:"models"`
	Buckets  int    `json:"buckets"`
	Problem  string `json:"problem,omitempty"`
}

// ---------------------------------------------------------------------------
// the Hub

type hubWhoami struct {
	Name string `json:"name"`
	Orgs []struct {
		Name string `json:"name"`
	} `json:"orgs"`
	Auth struct {
		Type        string `json:"type"`
		AccessToken struct {
			Role string `json:"role"`
		} `json:"accessToken"`
	} `json:"auth"`
}

// role is the token's access: read, write, fineGrained, or oauth for a
// sign-in from hf auth login, which has the account's own access.
func (w hubWhoami) role() string {
	if w.Auth.AccessToken.Role == "" && w.Auth.Type == "oauth" {
		return "oauth"
	}
	return w.Auth.AccessToken.Role
}

// dataTokenOwner is whose token reads a data organization.
type dataTokenOwner struct {
	Account string    `json:"account"`
	Role    string    `json:"role,omitempty"`
	Checked time.Time `json:"checked"`
}

var hubClient = &http.Client{Timeout: 30 * time.Second}

// hubGet calls the Hub with a token and decodes the answer. It returns
// the Link header's next page, if any.
func hubGet(token, target string, result any) (string, error) {
	if strings.HasPrefix(target, "/") {
		target = dataHubURL + target
	}
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "hi/"+version)
	response, err := hubClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("can't reach Hugging Face: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return "", err
	}
	switch {
	case response.StatusCode == http.StatusUnauthorized:
		return "", errors.New("Hugging Face refused the token (401); it may be revoked or mistyped")
	case response.StatusCode >= 300:
		var problem struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &problem) == nil && problem.Error != "" {
			return "", fmt.Errorf("Hugging Face: %s (%d)", problem.Error, response.StatusCode)
		}
		return "", fmt.Errorf("Hugging Face answered %s", response.Status)
	}
	return linkNext(response.Header.Get("Link")), json.Unmarshal(data, result)
}

var linkNextPattern = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="next"`)

func linkNext(header string) string {
	if match := linkNextPattern.FindStringSubmatch(header); match != nil {
		return match[1]
	}
	return ""
}

func hubWhoamiCall(token string) (hubWhoami, error) {
	var who hubWhoami
	_, err := hubGet(token, "/api/whoami-v2", &who)
	return who, err
}

// hubList fetches an organization's datasets, models, and buckets.
func hubList(token, org string) ([]dataItem, error) {
	var items []dataItem
	for _, kind := range []string{"dataset", "model"} {
		next := "/api/" + kind + "s?author=" + url.QueryEscape(org) +
			"&limit=1000&expand[]=private&expand[]=lastModified&expand[]=sha"
		if kind == "dataset" {
			next += "&expand[]=mainSize"
		}
		for page := 0; next != "" && page < dataListPages; page++ {
			var repos []struct {
				ID           string    `json:"id"`
				Private      bool      `json:"private"`
				LastModified time.Time `json:"lastModified"`
				SHA          string    `json:"sha"`
				MainSize     int64     `json:"mainSize"`
			}
			var err error
			if next, err = hubGet(token, next, &repos); err != nil {
				return nil, err
			}
			for _, repo := range repos {
				items = append(items, dataItem{Kind: kind, ID: repo.ID, Private: repo.Private,
					Size: repo.MainSize, Updated: repo.LastModified, Commit: repo.SHA})
			}
		}
	}
	next := "/api/buckets/" + url.PathEscape(org)
	for page := 0; next != "" && page < dataListPages; page++ {
		var buckets []struct {
			ID         string    `json:"id"`
			Private    bool      `json:"private"`
			UpdatedAt  time.Time `json:"updatedAt"`
			Size       int64     `json:"size"`
			TotalFiles int       `json:"totalFiles"`
		}
		var err error
		if next, err = hubGet(token, next, &buckets); err != nil {
			return nil, err
		}
		for _, bucket := range buckets {
			items = append(items, dataItem{Kind: "bucket", ID: bucket.ID, Private: bucket.Private,
				Size: bucket.Size, Updated: bucket.UpdatedAt, Files: bucket.TotalFiles})
		}
	}
	sortDataItems(items)
	return items, nil
}

func sortDataItems(items []dataItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].ID != items[j].ID {
			return strings.ToLower(items[i].ID) < strings.ToLower(items[j].ID)
		}
		return items[i].Kind < items[j].Kind
	})
}

// ---------------------------------------------------------------------------
// organizations

var dataOrgPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)

func (s *hiServer) dataOrgs() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	orgs := map[string]string{}
	for name, token := range s.keys {
		if org, ok := strings.CutPrefix(name, dataKeyPrefix); ok {
			orgs[org] = token
		}
	}
	return orgs
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// checkDataOrg checks that a token reads an organization and counts what
// it can see.
func checkDataOrg(token, org string, who hubWhoami) apiDataOrgStatus {
	status := apiDataOrgStatus{Org: org, Account: who.Name, Role: who.role()}
	member := strings.EqualFold(who.Name, org)
	for _, candidate := range who.Orgs {
		member = member || strings.EqualFold(candidate.Name, org)
	}
	if !member {
		status.Problem = fmt.Sprintf("the token's account %s is not a member of %s", who.Name, org)
		return status
	}
	items, err := hubList(token, org)
	if err != nil {
		status.Problem = err.Error()
		return status
	}
	status.Datasets, status.Models, status.Buckets = countDataKinds(items)
	return status
}

func countDataKinds(items []dataItem) (datasets, models, buckets int) {
	for _, item := range items {
		switch item.Kind {
		case "dataset":
			datasets++
		case "model":
			models++
		case "bucket":
			buckets++
		}
	}
	return datasets, models, buckets
}

// addDataOrgs checks the token for each organization and stores it. Either
// all are added or none.
func (s *hiServer) addDataOrgs(orgs []string, token, actor string) ([]apiDataOrgStatus, error) {
	if len(orgs) == 0 {
		return nil, serverUsageError{"name at least one organization"}
	}
	for _, org := range orgs {
		if !dataOrgPattern.MatchString(org) {
			return nil, serverUsageError{fmt.Sprintf("%q is not a Hugging Face organization name", org)}
		}
	}
	if strings.TrimSpace(token) == "" {
		return nil, serverUsageError{"no token was given"}
	}
	who, err := hubWhoamiCall(token)
	if err != nil {
		return nil, err
	}
	var results []apiDataOrgStatus
	for _, org := range orgs {
		status := checkDataOrg(token, org, who)
		if status.Problem != "" {
			return nil, fmt.Errorf("nothing was added: %s: %s", org, status.Problem)
		}
		results = append(results, status)
	}
	s.mu.Lock()
	for _, org := range orgs {
		s.keys[dataKeyPrefix+org] = token
	}
	err = s.saveKeysLocked()
	s.recordDataTokenOwnersLocked(results)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s.dataMu.Lock()
	for _, org := range orgs {
		delete(s.dataLists, org)
	}
	s.dataMu.Unlock()
	for _, status := range results {
		s.audit(actor, "connected data organization", status.Org, fmt.Sprintf("token of %s (%s); %s, %s, %s",
			status.Account, status.Role, countNoun(status.Datasets, "dataset"), countNoun(status.Models, "model"), countNoun(status.Buckets, "bucket")))
	}
	return results, nil
}

func (s *hiServer) removeDataOrg(org, actor string) error {
	s.mu.Lock()
	if _, ok := s.keys[dataKeyPrefix+org]; !ok {
		s.mu.Unlock()
		return serverUsageError{fmt.Sprintf("no data organization named %q", org)}
	}
	delete(s.keys, dataKeyPrefix+org)
	err := s.saveKeysLocked()
	if s.state.DataTokens[org] != nil {
		delete(s.state.DataTokens, org)
		s.saveLocked()
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.dataMu.Lock()
	delete(s.dataLists, org)
	s.dataMu.Unlock()
	s.audit(actor, "removed data organization", org, "")
	return nil
}

// testDataOrgs checks every organization's token, or one's, now.
func (s *hiServer) testDataOrgs(only string) ([]apiDataOrgStatus, error) {
	orgs := s.dataOrgs()
	if only != "" {
		token, ok := orgs[only]
		if !ok {
			return nil, serverUsageError{fmt.Sprintf("no data organization named %q", only)}
		}
		orgs = map[string]string{only: token}
	}
	results := []apiDataOrgStatus{}
	for _, org := range sortedKeys(orgs) {
		who, err := hubWhoamiCall(orgs[org])
		if err != nil {
			results = append(results, apiDataOrgStatus{Org: org, Problem: err.Error()})
			continue
		}
		results = append(results, checkDataOrg(orgs[org], org, who))
	}
	s.mu.Lock()
	s.recordDataTokenOwnersLocked(results)
	s.mu.Unlock()
	return results, nil
}

func (s *hiServer) recordDataTokenOwnersLocked(results []apiDataOrgStatus) {
	if s.state.DataTokens == nil {
		s.state.DataTokens = map[string]*dataTokenOwner{}
	}
	for _, status := range results {
		if status.Account != "" {
			s.state.DataTokens[status.Org] = &dataTokenOwner{Account: status.Account, Role: status.Role, Checked: computeNow().UTC()}
		}
	}
	s.saveLocked()
}

// dataListings returns every organization's listing, fetching those older
// than dataListEvery.
func (s *hiServer) dataListings() map[string]dataListing {
	orgs := s.dataOrgs()
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	if s.dataLists == nil {
		s.dataLists = map[string]*dataListing{}
	}
	result := map[string]dataListing{}
	for _, org := range sortedKeys(orgs) {
		listing := s.dataLists[org]
		if listing == nil || time.Since(listing.fetched) > dataListEvery {
			items, err := hubList(orgs[org], org)
			fresh := &dataListing{fetched: time.Now(), items: items}
			if err != nil {
				fresh.problem = err.Error()
				if listing != nil {
					fresh.items = listing.items // keep showing the last good list
				}
			}
			listing = fresh
			s.dataLists[org] = listing
		}
		result[org] = *listing
	}
	return result
}

// ---------------------------------------------------------------------------
// policy

// dataAllowed reports whether a group may read a repository: everything
// unless the group's policy lists patterns.
func dataAllowed(policy serverPolicy, group, id string) bool {
	rules, ok := policy.Groups[group]
	if !ok || rules.Data == nil {
		return true
	}
	for _, pattern := range *rules.Data {
		if pattern == "*" {
			return true
		}
		if matched, _ := path.Match(strings.ToLower(pattern), strings.ToLower(id)); matched {
			return true
		}
	}
	return false
}

func (s *hiServer) userGroup(user string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.state.Users[user]
	if record == nil || record.Kind == "viewer" {
		return "", false
	}
	return record.Group, true
}

// ---------------------------------------------------------------------------
// hi data tokens

// dataClaims is what a hi data token carries, signed with the server key so
// it survives a restart and needs no storage.
type dataClaims struct {
	Device  string `json:"d"`
	User    string `json:"u"`
	Scope   string `json:"s,omitempty"` // "*" or "<kind>:<org>/<name>"
	Expires int64  `json:"e"`
	// A run token, for a cloud machine (hi server expose): accepted only on
	// the instance listener, for the repositories in Scopes.
	Cloud  bool     `json:"x,omitempty"`
	Scopes []string `json:"ss,omitempty"`
	Run    string   `json:"r,omitempty"`
}

// allows reports whether the token covers a repository.
func (c dataClaims) allows(kind, id string) bool {
	if c.Cloud {
		for _, scope := range c.Scopes {
			if strings.EqualFold(scope, kind+":"+id) {
				return true
			}
		}
		return false
	}
	return c.Scope == "*" || strings.EqualFold(c.Scope, kind+":"+id)
}

func (c dataClaims) describeScope() string {
	if c.Cloud {
		return strings.Join(c.Scopes, ", ")
	}
	return c.Scope
}

func dataTokenPayload(claims []byte) []byte {
	return append([]byte("hi data token v1\n"), claims...)
}

func (s *hiServer) issueDataToken(claims dataClaims) string {
	data, _ := json.Marshal(claims)
	s.mu.Lock()
	key := s.serverKey
	s.mu.Unlock()
	signature := ed25519.Sign(key, dataTokenPayload(data))
	return dataTokenPrefix + base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// verifyDataToken checks a token's signature and time, and that its device
// and user are still enrolled.
func (s *hiServer) verifyDataToken(token string) (dataClaims, error) {
	var claims dataClaims
	body, ok := strings.CutPrefix(token, dataTokenPrefix)
	encoded, encodedSignature, found := strings.Cut(body, ".")
	if !ok || !found {
		return claims, errors.New("not a hi data token; run the command through `hi data`")
	}
	data, err1 := base64.RawURLEncoding.DecodeString(encoded)
	signature, err2 := base64.RawURLEncoding.DecodeString(encodedSignature)
	s.mu.Lock()
	public := s.serverKey.Public().(ed25519.PublicKey)
	s.mu.Unlock()
	if err1 != nil || err2 != nil || !ed25519.Verify(public, dataTokenPayload(data), signature) || json.Unmarshal(data, &claims) != nil {
		return claims, errors.New("the hi data token is not valid for this server")
	}
	if computeNow().Unix() > claims.Expires {
		return claims, errors.New("the hi data token has expired; run the command through `hi data` again")
	}
	s.mu.Lock()
	device := s.state.Devices[claims.Device]
	user := s.state.Users[claims.User]
	s.mu.Unlock()
	if device == nil || user == nil || device.User != claims.User {
		return claims, errors.New("the device or user of this hi data token is no longer enrolled")
	}
	return claims, nil
}

// parseDataScope splits "dataset:org/name". "*" is every repository.
func parseDataScope(scope string) (kind, id string, err error) {
	if scope == "*" {
		return "", "", nil
	}
	kind, id, _ = strings.Cut(scope, ":")
	org, name, found := strings.Cut(id, "/")
	if !containsString(dataKinds, kind) || !found || !validRepoPart(org) || !validRepoPart(name) {
		return "", "", fmt.Errorf("invalid scope %q; expected dataset:, model:, or bucket:<org>/<name>", scope)
	}
	return kind, id, nil
}

var repoPartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)

func validRepoPart(part string) bool {
	return repoPartPattern.MatchString(part) && !strings.Contains(part, "..")
}

// ---------------------------------------------------------------------------
// device API

func (s *hiServer) dataRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/data", s.device(s.handleDataCatalog))
	mux.HandleFunc("POST /v1/data/token", s.device(s.handleDataToken))
	mux.HandleFunc("POST /v1/data/links", s.device(s.handleDataLinks))
	mux.HandleFunc("POST /v1/data/run-access", s.device(s.handleRunAccess))
	mux.Handle(dataProxyPath+"/", http.HandlerFunc(s.handleDataProxy))
}

// dataBucketsFrom is the first hi version that downloads buckets; earlier
// ones would ask hf download for a model of the same name.
var dataBucketsFrom = [3]int{0, 24, 2}

// clientKnowsBuckets reads the version from the User-Agent. Development
// builds and unknown agents get everything.
func clientKnowsBuckets(agent string) bool {
	text, ok := strings.CutPrefix(agent, "hi/v")
	if !ok {
		return true
	}
	parts := strings.SplitN(text, ".", 3)
	if len(parts) != 3 {
		return true
	}
	for i, part := range parts {
		digits, _, _ := strings.Cut(part, "-")
		number, err := strconv.Atoi(digits)
		if err != nil {
			return true
		}
		if number != dataBucketsFrom[i] {
			return number > dataBucketsFrom[i]
		}
	}
	return true
}

func (s *hiServer) handleDataCatalog(w http.ResponseWriter, r *http.Request, device serverDevice, _ []byte) {
	group, ok := s.userGroup(device.User)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "a dashboard viewer can't use hi data")
		return
	}
	policy := s.policy()
	catalog := apiDataCatalog{Orgs: []apiDataOrg{}, Items: []dataItem{}}
	listings := s.dataListings()
	for _, org := range sortedKeys(listings) {
		listing := listings[org]
		entry := apiDataOrg{Name: org, Problem: listing.problem}
		var allowed []dataItem
		buckets := clientKnowsBuckets(r.UserAgent())
		for _, item := range listing.items {
			if item.Kind == "bucket" && !buckets {
				continue
			}
			if dataAllowed(policy, group, item.ID) {
				allowed = append(allowed, item)
			}
		}
		catalog.Items = append(catalog.Items, allowed...)
		entry.Datasets, entry.Models, entry.Buckets = countDataKinds(allowed)
		catalog.Orgs = append(catalog.Orgs, entry)
	}
	writeJSON(w, http.StatusOK, catalog)
}

func (s *hiServer) handleDataToken(w http.ResponseWriter, _ *http.Request, device serverDevice, body []byte) {
	group, ok := s.userGroup(device.User)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "a dashboard viewer can't use hi data")
		return
	}
	var input struct {
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, "the request is not JSON")
		return
	}
	_, id, err := parseDataScope(input.Scope)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if id != "" {
		org, _, _ := strings.Cut(id, "/")
		if _, ok := s.dataOrgs()[org]; !ok {
			writeAPIError(w, http.StatusNotFound, fmt.Sprintf("this server doesn't serve the %s organization", org))
			return
		}
		if !dataAllowed(s.policy(), group, id) {
			writeAPIError(w, http.StatusForbidden, fmt.Sprintf("your group %s may not read %s", group, id))
			return
		}
	}
	expires := computeNow().Add(dataTokenLifetime)
	token := s.issueDataToken(dataClaims{Device: device.Fingerprint, User: device.User, Scope: input.Scope, Expires: expires.Unix()})
	s.audit(device.User, "data token", input.Scope, "on "+device.Hostname)
	writeJSON(w, http.StatusOK, apiDataToken{Token: token, Expires: expires, Path: dataProxyPath})
}

// ---------------------------------------------------------------------------
// the proxy

// dataRoute is a proxied call the server allows.
type dataRoute struct {
	kind     string
	id       string
	download bool // a file's resolve call, recorded in data_usage.jsonl
	file     string
	// xet is a Xet read token: the device then reads the repository's files
	// from Hugging Face's storage directly, so this is all the server sees.
	xet      bool
	revision string
}

// parseDataRoute allows only the Hub's read calls that hf download,
// hf buckets sync, and huggingface_hub make for one dataset, model, or
// bucket.
func parseDataRoute(method, escapedPath string) (dataRoute, bool) {
	// The Hub encodes a nested file name as one segment (onnx%2Fconfig.json),
	// so encoded slashes are allowed, but no segment may climb out of the
	// repository, decoded or not. The fixed words and the organization and
	// name are compared exactly, so they can't hide an encoded slash.
	parts := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
	for _, part := range parts {
		decoded, err := url.PathUnescape(part)
		if part == "" || err != nil {
			return dataRoute{}, false
		}
		for _, piece := range strings.FieldsFunc(decoded, func(r rune) bool { return r == '/' || r == '\\' }) {
			if piece == "." || piece == ".." {
				return dataRoute{}, false
			}
		}
		if decoded == "." || decoded == ".." {
			return dataRoute{}, false
		}
	}
	read := method == http.MethodGet || method == http.MethodHead
	repo := func(kind, org, name string) (dataRoute, bool) {
		if !validRepoPart(org) || !validRepoPart(name) {
			return dataRoute{}, false
		}
		return dataRoute{kind: kind, id: org + "/" + name}, true
	}
	kindOf := map[string]string{"datasets": "dataset", "models": "model", "buckets": "bucket"}
	switch {
	// /api/datasets/<org>/<name>[/revision|tree|paths-info|xet-read-token/...],
	// and the same for models and buckets
	case len(parts) >= 4 && parts[0] == "api" && kindOf[parts[1]] != "":
		route, ok := repo(kindOf[parts[1]], parts[2], parts[3])
		if !ok {
			return route, false
		}
		if len(parts) == 4 {
			return route, read
		}
		switch parts[4] {
		case "revision", "tree":
			return route, read
		case "xet-read-token":
			route.xet = true
			if len(parts) > 5 {
				route.revision, _ = url.PathUnescape(parts[5])
			}
			return route, read
		case "paths-info":
			return route, method == http.MethodPost
		}
		return route, false
	// /api/resolve-cache/datasets/<org>/<name>/<commit>/<file>: where the Hub
	// sends resolve calls for small files.
	case len(parts) >= 7 && parts[0] == "api" && parts[1] == "resolve-cache" && kindOf[parts[2]] != "":
		route, ok := repo(kindOf[parts[2]], parts[3], parts[4])
		route.download, route.file = true, strings.Join(parts[6:], "/")
		return route, ok && read
	// /datasets/<org>/<name>/resolve/<rev>/<file>
	case len(parts) >= 6 && parts[0] == "datasets" && parts[3] == "resolve":
		route, ok := repo("dataset", parts[1], parts[2])
		route.download, route.file = true, strings.Join(parts[5:], "/")
		return route, ok && read
	// /buckets/<org>/<name>/resolve/<file>: buckets have no revisions
	case len(parts) >= 5 && parts[0] == "buckets" && parts[3] == "resolve":
		route, ok := repo("bucket", parts[1], parts[2])
		route.download, route.file = true, strings.Join(parts[4:], "/")
		return route, ok && read
	// /<org>/<name>/resolve/<rev>/<file>, a model
	case len(parts) >= 5 && parts[2] == "resolve" && !containsString([]string{"api", "datasets", "spaces", "buckets"}, parts[0]):
		route, ok := repo("model", parts[0], parts[1])
		route.download, route.file = true, strings.Join(parts[4:], "/")
		return route, ok && read
	}
	return dataRoute{}, false
}

// writeHubError answers in the Hub's error shape, which huggingface_hub
// shows to the user.
func writeHubError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("X-Error-Message", message)
	writeJSON(w, status, map[string]string{"error": message})
}

func (s *hiServer) handleDataProxy(w http.ResponseWriter, r *http.Request) {
	s.serveDataProxy(w, r, false, "")
}

// serveDataProxy is the proxy on the client listener (device tokens) or,
// with cloud set, on the instance listener (run tokens only), whose links
// point at the public URL.
func (s *hiServer) serveDataProxy(w http.ResponseWriter, r *http.Request, cloud bool, public string) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims, err := s.verifyDataToken(strings.TrimSpace(token))
	if err == nil && claims.Cloud && s.runRevoked(claims.Run) {
		err = fmt.Errorf("the run token of %s was revoked on the hi server", claims.Run)
	}
	if err == nil && claims.Cloud != cloud {
		err = errors.New("this token is for a cloud machine's run, which reaches the server through its public URL")
		if cloud {
			err = errors.New("only run tokens are accepted here; a device's hi data token works only inside NetBird")
		}
	}
	if err != nil {
		writeHubError(w, http.StatusUnauthorized, err.Error())
		return
	}
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), dataProxyPath)
	route, ok := parseDataRoute(r.Method, escaped)
	if !ok {
		writeHubError(w, http.StatusForbidden, fmt.Sprintf("hi data only passes on downloads of datasets, models, and buckets; %s %s is not one", r.Method, escaped))
		return
	}
	if !claims.allows(route.kind, route.id) {
		writeHubError(w, http.StatusForbidden, fmt.Sprintf("this hi data token is for %s, not %s", claims.describeScope(), route.id))
		return
	}
	group, ok := s.userGroup(claims.User)
	if !ok || !dataAllowed(s.policy(), group, route.id) {
		writeHubError(w, http.StatusForbidden, fmt.Sprintf("your group may not read %s", route.id))
		return
	}
	org, _, _ := strings.Cut(route.id, "/")
	hubToken, ok := s.dataOrgs()[org]
	if !ok {
		// Organization names are case-insensitive on the Hub.
		for name, candidate := range s.dataOrgs() {
			if strings.EqualFold(name, org) {
				hubToken, ok = candidate, true
			}
		}
	}
	if !ok {
		writeHubError(w, http.StatusNotFound, fmt.Sprintf("this server doesn't serve the %s organization", org))
		return
	}

	hub, err := url.Parse(dataHubURL)
	if err != nil {
		writeHubError(w, http.StatusInternalServerError, "the Hub address is not valid")
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	base := scheme + "://" + r.Host + dataProxyPath
	if public != "" {
		base = public + dataProxyPath
	}
	upstreamPath, _ := url.PathUnescape(escaped)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(out *httputil.ProxyRequest) {
			out.Out.URL = &url.URL{Scheme: hub.Scheme, Host: hub.Host, Path: hub.Path + upstreamPath,
				RawPath: hub.Path + escaped, RawQuery: r.URL.RawQuery}
			out.Out.Host = hub.Host
			out.Out.Header.Set("Authorization", "Bearer "+hubToken)
			out.Out.Header.Del("Cookie")
		},
		ModifyResponse: func(response *http.Response) error {
			// Keep the device talking to the proxy for the Hub's own links;
			// CDN and storage links go out unchanged.
			if location := response.Header.Get("Location"); location != "" {
				response.Header.Set("Location", rewriteHubLink(location, base))
			}
			if links := response.Header.Values("Link"); len(links) > 0 {
				response.Header.Del("Link")
				for _, link := range links {
					response.Header.Add("Link", strings.ReplaceAll(link, strings.TrimRight(dataHubURL, "/"), base))
				}
			}
			// A redirect within the Hub (small files go to resolve-cache)
			// is recorded where it lands, so each file counts once.
			if route.xet {
				s.recordDataUsage(claims, route, "xet", response)
			} else if route.download && !(response.StatusCode >= 300 && response.StatusCode < 400 && strings.HasPrefix(location(response), base)) {
				s.recordDataUsage(claims, route, r.Method, response)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeHubError(w, http.StatusBadGateway, "the hi server can't reach Hugging Face: "+err.Error())
		},
		FlushInterval: -1,
	}
	proxy.ServeHTTP(w, r)
}

func location(response *http.Response) string { return response.Header.Get("Location") }

func rewriteHubLink(location, base string) string {
	hub := strings.TrimRight(dataHubURL, "/")
	switch {
	case strings.HasPrefix(location, "/") && !strings.HasPrefix(location, "//"):
		return base + location
	case strings.HasPrefix(location, hub+"/"):
		return base + strings.TrimPrefix(location, hub)
	}
	return location
}

// dataUsage is one line of data_usage.jsonl: a file a device resolved.
// The bytes come from the CDN, so the size is the Hub's, not a count.
type dataUsage struct {
	Time     time.Time `json:"time"`
	User     string    `json:"user"`
	Device   string    `json:"device"`
	Kind     string    `json:"kind"`
	Repo     string    `json:"repo"`
	File     string    `json:"file,omitempty"`
	Revision string    `json:"revision,omitempty"`
	Method   string    `json:"method"`
	Status   int       `json:"status"`
	Size     int64     `json:"size,omitempty"`
}

func (s *hiServer) recordDataUsage(claims dataClaims, route dataRoute, method string, response *http.Response) {
	size, _ := strconv.ParseInt(response.Header.Get("X-Linked-Size"), 10, 64)
	if size == 0 && response.StatusCode == http.StatusOK && !route.xet {
		size = response.ContentLength
	}
	file, _ := url.PathUnescape(route.file)
	line, _ := json.Marshal(dataUsage{Time: computeNow().UTC(), User: claims.User, Device: claims.Device,
		Kind: route.kind, Repo: route.id, File: file, Revision: route.revision, Method: method, Status: response.StatusCode, Size: max(size, 0)})
	s.dataMu.Lock()
	defer s.dataMu.Unlock()
	handle, err := os.OpenFile(filepath.Join(s.dir, "data_usage.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		handle.Write(append(line, '\n'))
		handle.Close()
	}
}

// ---------------------------------------------------------------------------
// admin API and commands

func (s *hiServer) dataAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/data", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.dataOrgList())
	})
	mux.HandleFunc("POST /admin/data", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			As    string
			Orgs  []string
			Token string
		}
		json.NewDecoder(io.LimitReader(r.Body, serverMaxBody)).Decode(&input)
		results, err := s.addDataOrgs(input.Orgs, input.Token, input.As)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, results)
	})
	mux.HandleFunc("DELETE /admin/data/{org}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.removeDataOrg(r.PathValue("org"), r.URL.Query().Get("as")); err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /admin/data/test", func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Org string }
		json.NewDecoder(io.LimitReader(r.Body, serverMaxBody)).Decode(&input)
		results, err := s.testDataOrgs(input.Org)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, results)
	})
}

// dataOrgList is the organizations with what was last listed for them.
func (s *hiServer) dataOrgList() []apiDataOrgStatus {
	list := []apiDataOrgStatus{}
	listings := s.dataListings()
	for _, org := range sortedKeys(s.dataOrgs()) {
		listing := listings[org]
		status := apiDataOrgStatus{Org: org, Problem: listing.problem}
		status.Datasets, status.Models, status.Buckets = countDataKinds(listing.items)
		s.mu.Lock()
		if owner := s.state.DataTokens[org]; owner != nil {
			status.Account, status.Role = owner.Account, owner.Role
		}
		s.mu.Unlock()
		list = append(list, status)
	}
	return list
}

const serverDataUsage = "usage: hi server data [add <org>... [--token-file <path>] | list | test [<org>] | remove <org>]"

func serverDataCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("data", stderr)
	as := flags.String("as", currentUserName(), "who is acting")
	tokenFile := flags.String("token-file", "", "read the Hugging Face token from this file")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	dir, err := serverDirectory(*dirFlag)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		return fmt.Errorf("no server in %s; run `hi server init` first", dir)
	}
	_, statErr := os.Stat(filepath.Join(dir, "admin.sock"))
	running := statErr == nil
	// Without a running server, work on its state directly; it reads the
	// result when it starts.
	var direct *hiServer
	server := func() (*hiServer, error) {
		if direct == nil {
			direct, err = openServer(dir, io.Discard)
		}
		return direct, err
	}
	ops := dataAdminOps{
		list: func() ([]apiDataOrgStatus, error) {
			var list []apiDataOrgStatus
			if running {
				return list, adminCall(*dirFlag, http.MethodGet, "/admin/data", nil, &list)
			}
			s, err := server()
			if err != nil {
				return nil, err
			}
			return s.dataOrgList(), nil
		},
		add: func(orgs []string, token string) ([]apiDataOrgStatus, error) {
			var results []apiDataOrgStatus
			if running {
				body := map[string]any{"as": *as, "orgs": orgs, "token": token}
				return results, adminCall(*dirFlag, http.MethodPost, "/admin/data", body, &results)
			}
			s, err := server()
			if err != nil {
				return nil, err
			}
			return s.addDataOrgs(orgs, token, *as)
		},
		test: func(org string) ([]apiDataOrgStatus, error) {
			var results []apiDataOrgStatus
			if running {
				return results, adminCall(*dirFlag, http.MethodPost, "/admin/data/test", map[string]string{"org": org}, &results)
			}
			s, err := server()
			if err != nil {
				return nil, err
			}
			return s.testDataOrgs(org)
		},
		remove: func(org string) error {
			if running {
				return adminCall(*dirFlag, http.MethodDelete, "/admin/data/"+url.PathEscape(org)+"?as="+url.QueryEscape(*as), nil, nil)
			}
			s, err := server()
			if err != nil {
				return err
			}
			return s.removeDataOrg(org, *as)
		},
	}

	switch {
	case len(positional) == 0:
		if !isTerminal(stdin) {
			return usageError{serverDataUsage}
		}
		return serverDataMenu(newMenuUI(stdin, stdout), ops, stdout)

	case positional[0] == "list" && len(positional) == 1:
		list, err := ops.list()
		if err != nil {
			return err
		}
		printDataOrgs(list, stdout)
		return nil

	case positional[0] == "add" && len(positional) >= 2:
		token, err := readDataToken(positional[1:], *tokenFile, stdin, stdout)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Checking the token with Hugging Face…")
		results, err := ops.add(positional[1:], token)
		if err != nil {
			return err
		}
		printDataOrgs(results, stdout)
		fmt.Fprintln(stdout, "Connected devices now see these in `hi data`.")
		return nil

	case positional[0] == "test" && len(positional) <= 2:
		org := ""
		if len(positional) == 2 {
			org = positional[1]
		}
		results, err := ops.test(org)
		if err != nil {
			return err
		}
		printDataOrgs(results, stdout)
		for _, result := range results {
			if result.Problem != "" {
				return exitStatusError{code: 1, message: "some organizations have a problem"}
			}
		}
		return nil

	case positional[0] == "remove" && len(positional) == 2:
		if err := ops.remove(positional[1]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Removed %s and its token. Downloads already on devices stay there.\n", positional[1])
		return nil
	}
	return usageError{serverDataUsage}
}

// dataAdminOps lets the command and the menu work the same with or
// without a running server.
type dataAdminOps struct {
	list   func() ([]apiDataOrgStatus, error)
	add    func(orgs []string, token string) ([]apiDataOrgStatus, error)
	test   func(org string) ([]apiDataOrgStatus, error)
	remove func(org string) error
}

func serverDataMenu(ui menuUI, ops dataAdminOps, stdout io.Writer) error {
	// The list is shown at the start and after a removal; adding and
	// testing print the organizations they checked.
	showList := true
	for {
		list, err := ops.list()
		if err != nil {
			return err
		}
		if showList {
			fmt.Fprintln(stdout)
			printDataOrgs(list, stdout)
		}
		showList = false
		choice, err := ui.choose("Hugging Face organizations for hi data", []string{
			"+  Add organizations and their token",
			"✓  Test the tokens",
			"−  Remove an organization",
			"✕  Quit",
		}, false)
		if err != nil {
			return nil
		}
		var actionErr error
		switch choice {
		case 0:
			var names string
			if names, actionErr = ui.input("Organizations, separated by spaces", "", nil); actionErr != nil {
				break
			}
			orgs := strings.Fields(strings.ReplaceAll(names, ",", " "))
			var token string
			if token, actionErr = ui.secret("Hugging Face token that reads " + strings.Join(orgs, ", ")); actionErr != nil {
				break
			}
			ui.command("hi server data add " + strings.Join(orgs, " "))
			var results []apiDataOrgStatus
			ui.busy("Checking the token with Hugging Face…", func() { results, actionErr = ops.add(orgs, token) })
			if actionErr == nil {
				printDataOrgs(results, stdout)
			}
		case 1:
			ui.command("hi server data test")
			var results []apiDataOrgStatus
			ui.busy("Checking each token…", func() { results, actionErr = ops.test("") })
			if actionErr == nil {
				printDataOrgs(results, stdout)
			}
		case 2:
			if len(list) == 0 {
				ui.note("No organizations to remove.")
				break
			}
			var names []string
			for _, org := range list {
				names = append(names, org.Org)
			}
			var picked int
			if picked, actionErr = ui.choose("Remove which organization?", names, false); actionErr != nil {
				break
			}
			ui.command("hi server data remove " + names[picked])
			actionErr = ops.remove(names[picked])
			showList = true
		default:
			return nil
		}
		if actionErr != nil && !errors.Is(actionErr, errMenuBack) {
			ui.failure(actionErr)
		}
	}
}

func printDataOrgs(list []apiDataOrgStatus, stdout io.Writer) {
	if len(list) == 0 {
		fmt.Fprintln(stdout, "No organizations yet; add one with `hi server data add <org>`.")
		return
	}
	for _, org := range list {
		if org.Problem != "" {
			fmt.Fprintf(stdout, "✗ %s: %s\n", org.Org, org.Problem)
			continue
		}
		line := fmt.Sprintf("✓ %s: %s, %s, %s", org.Org, countNoun(org.Datasets, "dataset"), countNoun(org.Models, "model"), countNoun(org.Buckets, "bucket"))
		if org.Account != "" {
			line += fmt.Sprintf(" (token of %s, %s)", org.Account, describeHubRole(org.Role))
		}
		fmt.Fprintln(stdout, line)
		switch org.Role {
		case "write":
			fmt.Fprintln(stdout, "  warning: this token can also write. hi data only passes on reads, but a fine-grained,")
			fmt.Fprintln(stdout, "  read-only token limited to these organizations is safer if the server box is compromised.")
		case "oauth":
			fmt.Fprintln(stdout, "  warning: this is a sign-in from hf auth login, with everything the account may do. hi data")
			fmt.Fprintln(stdout, "  only passes on reads, but a fine-grained, read-only token limited to these organizations")
			fmt.Fprintln(stdout, "  is safer if the server box is compromised.")
		}
	}
}

// countNoun is "1 bucket" or "3 buckets".
func countNoun(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

func describeHubRole(role string) string {
	switch role {
	case "read":
		return "read-only"
	case "fineGrained":
		return "fine-grained"
	case "oauth":
		return "a sign-in"
	case "":
		return "unknown access"
	}
	return role
}

// readDataToken reads the token from a file, without echo from a terminal,
// or from stdin in scripts. Never from an argument, which would end up in
// the shell history and the process list.
func readDataToken(orgs []string, file string, stdin io.Reader, stdout io.Writer) (string, error) {
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	if handle, ok := stdin.(*os.File); ok && isTerminal(stdin) {
		fmt.Fprintf(stdout, "Hugging Face token for %s (a fine-grained, read-only token is best; it shows as *): ", strings.Join(orgs, ", "))
		token, err := readSecret(handle, stdout)
		fmt.Fprintln(stdout)
		return strings.TrimSpace(string(token)), err
	}
	line, err := readLine(stdin)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if strings.TrimSpace(line) == "" {
		return "", errors.New("no token given; run this in a terminal, pass --token-file, or pipe the token on stdin")
	}
	return strings.TrimSpace(line), nil
}
