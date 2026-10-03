package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// What hi data does in a project: it records what was downloaded, at
// which commit, in .hifin/data.json, so `hi data get` with no arguments
// fetches the same files for a teammate or a compute instance; and it
// runs commands that read the team's data directly (hi data run, env).

const dataRecordFile = "data.json" // in .hifin/

// dataRecord is one download in .hifin/data.json.
type dataRecord struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	// Revision is the commit a dataset or model was downloaded at.
	Revision string `json:"revision,omitempty"`
	// To is the folder, relative to the project.
	To      string    `json:"to"`
	Include []string  `json:"include,omitempty"`
	Exclude []string  `json:"exclude,omitempty"`
	Fetched time.Time `json:"fetched"`
	// FilesHash and Files describe a bucket's files when it was
	// downloaded; buckets have no history, so this is how a later
	// download knows it changed.
	FilesHash string `json:"files_hash,omitempty"`
	Files     int    `json:"files,omitempty"`
}

type dataRecords struct {
	Data []dataRecord `json:"data"`
}

// dataProjectRoot is the nearest folder upwards with a .hifin folder.
func dataProjectRoot() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, ".hifin")); err == nil && info.IsDir() {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func loadDataRecords(root string) (dataRecords, error) {
	var records dataRecords
	data, err := os.ReadFile(filepath.Join(root, ".hifin", dataRecordFile))
	if errors.Is(err, os.ErrNotExist) {
		return records, nil
	}
	if err != nil {
		return records, err
	}
	if err := json.Unmarshal(data, &records); err != nil {
		return records, fmt.Errorf("read .hifin/%s: %w", dataRecordFile, err)
	}
	return records, nil
}

// saveDataRecord adds a download, replacing one of the same repository
// into the same folder.
func saveDataRecord(root string, record dataRecord) error {
	records, err := loadDataRecords(root)
	if err != nil {
		return err
	}
	kept := records.Data[:0]
	for _, existing := range records.Data {
		if existing.Kind != record.Kind || !strings.EqualFold(existing.ID, record.ID) || existing.To != record.To {
			kept = append(kept, existing)
		}
	}
	records.Data = append(kept, record)
	sort.SliceStable(records.Data, func(i, j int) bool { return records.Data[i].To < records.Data[j].To })
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(root, ".hifin", dataRecordFile)
	if err := os.WriteFile(path+".tmp", append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// ---------------------------------------------------------------------------
// asking the Hub, through the proxy

func dataProxyGet(endpoint, token, path string, result any) error {
	request, err := http.NewRequest(http.MethodGet, endpoint+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "hi/"+version)
	response, err := (&http.Client{Timeout: 2 * time.Minute}).Do(request)
	if err != nil {
		return fmt.Errorf("can't reach the hi server: %w", err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if response.StatusCode != http.StatusOK {
		var problem struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &problem) == nil && problem.Error != "" {
			return errors.New(problem.Error)
		}
		return fmt.Errorf("the server answered %s", response.Status)
	}
	return json.Unmarshal(data, result)
}

// dataResolveCommit turns a branch, a tag, or nothing (the default branch)
// into the commit to download, so what is recorded is what was fetched.
func dataResolveCommit(endpoint, token string, item dataItem, revision string) (string, error) {
	path := "/api/" + item.Kind + "s/" + item.ID
	if revision != "" {
		path += "/revision/" + url.PathEscape(revision)
	}
	var info struct {
		SHA string `json:"sha"`
	}
	if err := dataProxyGet(endpoint, token, path, &info); err != nil {
		return "", fmt.Errorf("can't find %s of %s: %w", firstNonEmpty(revision, "the default branch"), item.ID, err)
	}
	if info.SHA == "" {
		return "", fmt.Errorf("the Hub gave no commit for %s", item.ID)
	}
	return info.SHA, nil
}

// dataBucketState hashes a bucket's file list: path, size, and content
// hash of every file.
func dataBucketState(endpoint, token, id string) (hash string, files int, err error) {
	var tree []struct {
		Type    string `json:"type"`
		Path    string `json:"path"`
		Size    int64  `json:"size"`
		XetHash string `json:"xetHash"`
	}
	if err := dataProxyGet(endpoint, token, "/api/buckets/"+id+"/tree?recursive=true", &tree); err != nil {
		return "", 0, err
	}
	var lines []string
	for _, entry := range tree {
		if entry.Type == "file" {
			lines = append(lines, fmt.Sprintf("%s\t%d\t%s", entry.Path, entry.Size, entry.XetHash))
		}
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:]), len(lines), nil
}

// ---------------------------------------------------------------------------
// hi data get with no arguments

// dataRestore downloads everything .hifin/data.json records, at the
// recorded commits, into the recorded folders.
func dataRestore(client *serverClient, connection *serverConnection, stdin io.Reader, stdout, stderr io.Writer) error {
	root, ok := dataProjectRoot()
	if !ok {
		return usageError{"usage: hi data get <org>/<name>; with no name it fetches what .hifin/data.json records, and there is no .hifin folder here"}
	}
	records, err := loadDataRecords(root)
	if err != nil {
		return err
	}
	if len(records.Data) == 0 {
		return usageError{"nothing is recorded in .hifin/data.json yet; download with `hi data get <org>/<name>` in this project first"}
	}
	catalog, err := fetchDataCatalog(client)
	if err != nil {
		return err
	}
	var failed []string
	for _, record := range records.Data {
		item, err := resolveDataItem(catalog, record.Kind+":"+record.ID)
		if err == nil {
			to := filepath.Join(root, record.To)
			if cwd, err := os.Getwd(); err == nil {
				if relative, err := filepath.Rel(cwd, to); err == nil {
					to = relative
				}
			}
			options := dataGetOptions{to: to, revision: record.Revision,
				include: record.Include, exclude: record.Exclude, restore: &record}
			err = downloadData(client, connection, item, options, stdin, stdout, stderr)
		}
		if err != nil {
			fmt.Fprintf(stderr, "hi: %s: %v\n", record.ID, err)
			failed = append(failed, record.ID)
		}
	}
	if len(failed) > 0 {
		return exitStatusError{code: 1, message: "not downloaded: " + strings.Join(failed, ", ")}
	}
	return nil
}

// ---------------------------------------------------------------------------
// hi data run and hi data env

func dataRunCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return usageError{"usage: hi data run -- <command> [args]"}
	}
	client, connection, err := dataClient()
	if err != nil {
		return err
	}
	token, endpoint, err := dataToken(client, connection, "*")
	if err != nil {
		return err
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("%s: %w", args[0], err)
	}
	command := exec.Command(path, args[1:]...)
	command.Env = dataEnvironment(endpoint, token)
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exitStatusError{code: exit.ExitCode(), message: fmt.Sprintf("%s exited with status %d", args[0], exit.ExitCode())}
		}
		return err
	}
	return nil
}

func dataEnvCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		return usageError{"usage: hi data env    (then: eval \"$(hi data env)\")"}
	}
	client, connection, err := dataClient()
	if err != nil {
		return err
	}
	token, endpoint, err := dataToken(client, connection, "*")
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "export HF_ENDPOINT=%s\nexport HF_TOKEN=%s\nunset HUGGING_FACE_HUB_TOKEN HF_TOKEN_PATH\n",
		shellQuote(endpoint), shellQuote(token))
	if file, ok := stdout.(*os.File); ok && isTerminal(file) {
		fmt.Fprintln(stderr, "hi: use it with eval \"$(hi data env)\"; the token lasts a day and works only from this machine")
	}
	return nil
}
