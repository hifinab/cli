package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// hi data lists and downloads the Hugging Face datasets, models, and
// buckets a connected hi server serves. The server keeps the Hugging Face tokens; hf
// runs here with HF_ENDPOINT pointing to the server's proxy and a hi data
// token that lasts a day (docs/specs/approved/hi_data.md).

// dataHFCommand is the hf CLI; tests replace it.
var dataHFCommand = "hf"

func runData(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			printDataUsage(stdout)
			return 0
		case "ls", "list":
			return exitCode(dataListCommand(args[1:], stdout, stderr), stderr)
		case "info":
			return exitCode(dataInfoCommand(args[1:], stdout, stderr), stderr)
		case "get":
			return exitCode(dataGetCommand(args[1:], stdin, stdout, stderr), stderr)
		case "run":
			return exitCode(dataRunCommand(args[1:], stdin, stdout, stderr), stderr)
		case "env":
			return exitCode(dataEnvCommand(args[1:], stdout, stderr), stderr)
		default:
			fmt.Fprintf(stderr, "hi: unknown data command %q\n\n", args[0])
			printDataUsage(stderr)
			return 2
		}
	}
	if file, ok := stdout.(*os.File); ok && isTerminal(stdin) && isTerminal(file) {
		return exitCode(dataMenu(newMenuUI(stdin, stdout), stdin, stdout, stderr), stderr)
	}
	return exitCode(dataListCommand(nil, stdout, stderr), stderr)
}

func printDataUsage(w io.Writer) {
	fmt.Fprintln(w, `hi data downloads the team's Hugging Face datasets, models, and buckets
through the hi server this device is connected to. The server keeps the Hugging Face
tokens; hf runs here without one.

Usage:
  hi data                                 Search, pick one, and download it
  hi data ls [<org>] [--kind dataset|model|bucket] [--json]
                                          What this device may download
  hi data info <org>/<name>               Size, files, and last update
  hi data get <org>/<name> [--to DIR]     Download it with hf (default: ./data/<name>)
      [--revision REV] [--include GLOB]... [--exclude GLOB]... [--no-record]
  hi data get                             In a project: everything .hifin/data.json records
  hi data run -- <command> [args]         Run a command that reads the team's data directly
                                          (load_dataset, from_pretrained, hf:// paths)
  hi data env                             HF_ENDPOINT and HF_TOKEN for a shell:
                                          eval "$(hi data env)"

In a project (a folder with .hifin/), hi data get records the repository,
the commit it downloaded, and the folder in .hifin/data.json.

Buckets download with hf buckets sync and have no revisions. When several
share a name, put dataset:, model:, or bucket: in front.
Needs hf (pip install -U huggingface_hub) and a server set up with
hi server data add <org>.`)
}

// ---------------------------------------------------------------------------
// talking to the server

func dataClient() (*serverClient, *serverConnection, error) {
	connection, err := loadServerConnection()
	if err != nil {
		return nil, nil, err
	}
	if connection == nil {
		return nil, nil, errors.New("hi data needs a hi server; connect this device with `hi connect <server>`")
	}
	key, err := loadDeviceKey(false)
	if err != nil {
		return nil, nil, err
	}
	return newServerClient(connection.URL, key), connection, nil
}

func fetchDataCatalog(client *serverClient) (apiDataCatalog, error) {
	var catalog apiDataCatalog
	err := client.call(http.MethodGet, "/v1/data", nil, &catalog)
	return catalog, err
}

// dataToken asks the server for a hi data token and returns it with the
// HF_ENDPOINT to use it on.
func dataToken(client *serverClient, connection *serverConnection, scope string) (token, endpoint string, err error) {
	var answer apiDataToken
	if err := client.call(http.MethodPost, "/v1/data/token", map[string]string{"scope": scope}, &answer); err != nil {
		return "", "", err
	}
	return answer.Token, strings.TrimRight(connection.URL, "/") + answer.Path, nil
}

// resolveDataItem finds <org>/<name>, with an optional dataset:, model:,
// or bucket: in front, among what this device may read.
func resolveDataItem(catalog apiDataCatalog, ref string) (dataItem, error) {
	kind, id := "", ref
	if before, after, found := strings.Cut(ref, ":"); found {
		kind, id = before, after
		if !containsString(dataKinds, kind) {
			return dataItem{}, usageError{fmt.Sprintf("%q: put dataset:, model:, or bucket: in front, or nothing", ref)}
		}
	}
	if _, _, found := strings.Cut(id, "/"); !found {
		return dataItem{}, usageError{fmt.Sprintf("%q: name it as <org>/<name>, as on Hugging Face", ref)}
	}
	var matches []dataItem
	for _, item := range catalog.Items {
		if strings.EqualFold(item.ID, id) && (kind == "" || item.Kind == kind) {
			matches = append(matches, item)
		}
	}
	switch len(matches) {
	case 0:
		return dataItem{}, fmt.Errorf("%s is not among what you may download; `hi data ls` shows the list", ref)
	case 1:
		return matches[0], nil
	}
	var kinds []string
	for _, match := range matches {
		kinds = append(kinds, match.Kind+":"+id)
	}
	return dataItem{}, usageError{fmt.Sprintf("%s is more than one kind; say %s", id, strings.Join(kinds, " or "))}
}

// ---------------------------------------------------------------------------
// hi data ls

func dataListCommand(args []string, stdout, stderr io.Writer) error {
	flags := &flagSet{newComputeFlags("data ls", stderr)}
	kind := flags.String("kind", "", "only datasets, models, or buckets")
	asJSON := flags.Bool("json", false, "print JSON")
	positional, err := flags.parse(args)
	if err != nil || len(positional) > 1 {
		return usageError{"usage: hi data ls [<org>] [--kind dataset|model|bucket] [--json]"}
	}
	if *kind != "" && !containsString(dataKinds, *kind) {
		return usageError{"--kind is dataset, model, or bucket"}
	}
	client, _, err := dataClient()
	if err != nil {
		return err
	}
	catalog, err := fetchDataCatalog(client)
	if err != nil {
		return err
	}
	org := ""
	if len(positional) == 1 {
		org = positional[0]
	}
	catalog = filterDataCatalog(catalog, org, *kind)
	if *asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(catalog)
	}
	printDataCatalog(catalog, stdout, stderr)
	return nil
}

func filterDataCatalog(catalog apiDataCatalog, org, kind string) apiDataCatalog {
	filtered := apiDataCatalog{Orgs: []apiDataOrg{}, Items: []dataItem{}}
	for _, entry := range catalog.Orgs {
		if org == "" || strings.EqualFold(entry.Name, org) {
			filtered.Orgs = append(filtered.Orgs, entry)
		}
	}
	for _, item := range catalog.Items {
		itemOrg, _, _ := strings.Cut(item.ID, "/")
		if (org == "" || strings.EqualFold(itemOrg, org)) && (kind == "" || item.Kind == kind) {
			filtered.Items = append(filtered.Items, item)
		}
	}
	return filtered
}

func printDataCatalog(catalog apiDataCatalog, stdout, stderr io.Writer) {
	for _, org := range catalog.Orgs {
		if org.Problem != "" {
			fmt.Fprintf(stderr, "hi: warning: %s: %s\n", org.Name, org.Problem)
		}
	}
	if len(catalog.Orgs) == 0 {
		fmt.Fprintln(stdout, "This server serves no Hugging Face organizations yet; an admin adds one with `hi server data add <org>`.")
		return
	}
	if len(catalog.Items) == 0 {
		fmt.Fprintln(stdout, "Nothing for you to download here.")
		return
	}
	table := newTable(stdout)
	fmt.Fprintln(table, "KIND\tNAME\tSIZE\tUPDATED")
	for _, item := range catalog.Items {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", item.Kind, item.ID, formatDataSize(item.Size), describeDataAge(item.Updated))
	}
	table.Flush()
}

func formatDataSize(size int64) string {
	if size <= 0 {
		return "-"
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	value, unit := float64(size), 0
	for value >= 1000 && unit < len(units)-1 {
		value /= 1000
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d B", size)
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

func describeDataAge(updated time.Time) string {
	if updated.IsZero() {
		return "-"
	}
	elapsed := computeNow().Sub(updated)
	day := 24 * time.Hour
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit + " ago"
		}
		return fmt.Sprintf("%d %ss ago", n, unit)
	}
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return plural(int(elapsed/time.Minute), "minute")
	case elapsed < day:
		return plural(int(elapsed/time.Hour), "hour")
	case elapsed < 60*day:
		return plural(int(elapsed/day), "day")
	case elapsed < 730*day:
		return plural(int(elapsed/(30*day)), "month")
	}
	return plural(int(elapsed/(365*day)), "year")
}

// ---------------------------------------------------------------------------
// hi data info

func dataInfoCommand(args []string, stdout, stderr io.Writer) error {
	flags := &flagSet{newComputeFlags("data info", stderr)}
	positional, err := flags.parse(args)
	if err != nil || len(positional) != 1 {
		return usageError{"usage: hi data info <org>/<name>"}
	}
	client, connection, err := dataClient()
	if err != nil {
		return err
	}
	catalog, err := fetchDataCatalog(client)
	if err != nil {
		return err
	}
	item, err := resolveDataItem(catalog, positional[0])
	if err != nil {
		return err
	}
	token, endpoint, err := dataToken(client, connection, item.Kind+":"+item.ID)
	if err != nil {
		return err
	}
	type dataFile struct {
		Name string `json:"rfilename"`
		Path string `json:"path"` // a bucket's
		Type string `json:"type"`
		Size int64  `json:"size"`
	}
	var info struct {
		Siblings []dataFile `json:"siblings"`
	}
	target := endpoint + "/api/" + item.Kind + "s/" + item.ID + "?blobs=true"
	if item.Kind == "bucket" {
		target = endpoint + "/api/buckets/" + item.ID + "/tree?recursive=true"
	}
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "hi/"+version)
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("can't reach the hi server at %s: %w", connection.URL, err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if response.StatusCode != http.StatusOK {
		var problem struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &problem) == nil && problem.Error != "" {
			return errors.New(problem.Error)
		}
		return fmt.Errorf("the server answered %s", response.Status)
	}
	if item.Kind == "bucket" {
		var tree []dataFile
		if err := json.Unmarshal(data, &tree); err != nil {
			return err
		}
		for _, entry := range tree {
			if entry.Type == "file" {
				entry.Name = entry.Path
				info.Siblings = append(info.Siblings, entry)
			}
		}
	} else if err := json.Unmarshal(data, &info); err != nil {
		return err
	}

	sort.Slice(info.Siblings, func(i, j int) bool { return info.Siblings[i].Size > info.Siblings[j].Size })
	var total int64
	for _, file := range info.Siblings {
		total += file.Size
	}
	fmt.Fprintf(stdout, "%s %s\n", item.Kind, item.ID)
	if item.Private {
		fmt.Fprintln(stdout, "  private")
	}
	fmt.Fprintf(stdout, "  %d files, %s\n", len(info.Siblings), formatDataSize(total))
	if item.Commit != "" {
		fmt.Fprintf(stdout, "  updated %s, at %s\n", describeDataAge(item.Updated), shortCommit(item.Commit))
	} else {
		fmt.Fprintf(stdout, "  updated %s\n", describeDataAge(item.Updated))
	}
	shown := min(len(info.Siblings), 15)
	if shown > 0 {
		fmt.Fprintln(stdout, "  largest files:")
		table := newTable(stdout)
		for _, file := range info.Siblings[:shown] {
			fmt.Fprintf(table, "    %s\t%s\n", file.Name, formatDataSize(file.Size))
		}
		table.Flush()
		if len(info.Siblings) > shown {
			fmt.Fprintf(stdout, "    … and %d more\n", len(info.Siblings)-shown)
		}
	}
	fmt.Fprintf(stdout, "Download it with `hi data get %s`.\n", item.ID)
	return nil
}

// ---------------------------------------------------------------------------
// hi data get

type dataGetOptions struct {
	to       string
	revision string
	include  []string
	exclude  []string
	noRecord bool
	// restore is the record being fetched again by hi data get with no
	// arguments; it is not recorded again.
	restore *dataRecord
}

func dataGetCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := &flagSet{newComputeFlags("data get", stderr)}
	var options dataGetOptions
	var include, exclude repeatedFlag
	flags.StringVar(&options.to, "to", "", "folder to download into")
	flags.StringVar(&options.revision, "revision", "", "branch, tag, or commit")
	flags.Var(&include, "include", "only files matching this pattern")
	flags.Var(&exclude, "exclude", "skip files matching this pattern")
	flags.BoolVar(&options.noRecord, "no-record", false, "don't record it in .hifin/data.json")
	positional, err := flags.parse(args)
	if err != nil || len(positional) > 1 || (len(positional) == 0 && len(args) > 0) {
		return usageError{"usage: hi data get <org>/<name> [--to DIR] [--revision REV] [--include GLOB]... [--exclude GLOB]... [--no-record]"}
	}
	options.include, options.exclude = include, exclude
	client, connection, err := dataClient()
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		return dataRestore(client, connection, stdin, stdout, stderr)
	}
	catalog, err := fetchDataCatalog(client)
	if err != nil {
		return err
	}
	item, err := resolveDataItem(catalog, positional[0])
	if err != nil {
		return err
	}
	return downloadData(client, connection, item, options, stdin, stdout, stderr)
}

func defaultDataFolder(item dataItem) string {
	_, name, _ := strings.Cut(item.ID, "/")
	return filepath.Join("data", name)
}

// dataGetArgs is the hf command for an item: hf download for datasets and
// models, hf buckets sync for buckets, which hf download refuses.
func dataGetArgs(item dataItem, options dataGetOptions) []string {
	var args []string
	switch item.Kind {
	case "bucket":
		args = []string{"buckets", "sync", "hf://buckets/" + item.ID, options.to}
	case "dataset":
		args = []string{"download", item.ID, "--repo-type", "dataset", "--local-dir", options.to}
	default:
		args = []string{"download", item.ID, "--local-dir", options.to}
	}
	if options.revision != "" {
		args = append(args, "--revision", options.revision)
	}
	// hf takes --include and --exclude once per pattern; after a single
	// flag, the next patterns would be read as file names.
	for _, pattern := range options.include {
		args = append(args, "--include", pattern)
	}
	for _, pattern := range options.exclude {
		args = append(args, "--exclude", pattern)
	}
	return args
}

// dataEnvironment is this process's environment with the device's own
// Hugging Face settings replaced by the server's endpoint and token.
func dataEnvironment(endpoint, token string) []string {
	var environment []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "HF_ENDPOINT", "HF_TOKEN", "HUGGING_FACE_HUB_TOKEN", "HF_TOKEN_PATH", "HF_HUB_OFFLINE":
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "HF_ENDPOINT="+endpoint, "HF_TOKEN="+token)
}

func downloadData(client *serverClient, connection *serverConnection, item dataItem, options dataGetOptions, stdin io.Reader, stdout, stderr io.Writer) error {
	hf, err := exec.LookPath(dataHFCommand)
	if err != nil {
		return errors.New("hf isn't installed; install it with `pip install -U huggingface_hub` (or `uv tool install huggingface_hub`) and run this again")
	}
	if item.Kind == "bucket" && options.revision != "" {
		return usageError{"buckets have no revisions; leave out --revision"}
	}
	if options.to == "" {
		options.to = defaultDataFolder(item)
	}
	token, endpoint, err := dataToken(client, connection, item.Kind+":"+item.ID)
	if err != nil {
		return err
	}
	// Pin a dataset or model to a commit, so what is recorded is exactly
	// what was fetched; note a bucket's files to tell later if they change.
	record := dataRecord{Kind: item.Kind, ID: item.ID, Include: options.include, Exclude: options.exclude}
	if item.Kind == "bucket" {
		if record.FilesHash, record.Files, err = dataBucketState(endpoint, token, item.ID); err != nil {
			return err
		}
		if previous := options.restore; previous != nil && previous.FilesHash != "" && previous.FilesHash != record.FilesHash {
			fmt.Fprintf(stderr, "hi: warning: bucket %s has changed since it was recorded on %s (%d files then, %d now); buckets keep no history, so this gets the current files\n",
				item.ID, previous.Fetched.Local().Format("2006-01-02 15:04"), previous.Files, record.Files)
		}
	} else {
		if record.Revision, err = dataResolveCommit(endpoint, token, item, options.revision); err != nil {
			return err
		}
		options.revision = record.Revision
	}
	args := dataGetArgs(item, options)
	at := ""
	if record.Revision != "" {
		at = " at " + shortCommit(record.Revision)
	}
	fmt.Fprintf(stderr, "Downloading %s %s%s into %s through %s\n", item.Kind, item.ID, at, options.to, connection.URL)
	command := exec.Command(hf, args...)
	command.Env = dataEnvironment(endpoint, token)
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exitStatusError{code: exit.ExitCode(), message: fmt.Sprintf("hf could not download %s", item.ID)}
		}
		return err
	}
	fmt.Fprintf(stderr, "Downloaded %s into %s\n", item.ID, options.to)
	if options.noRecord || options.restore != nil {
		return nil
	}
	root, ok := dataProjectRoot()
	if !ok {
		return nil
	}
	absolute, err := filepath.Abs(options.to)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		fmt.Fprintf(stderr, "hi: not recorded in .hifin/data.json: %s is outside the project\n", options.to)
		return nil
	}
	record.To, record.Fetched = filepath.ToSlash(relative), computeNow().UTC()
	if err := saveDataRecord(root, record); err != nil {
		return fmt.Errorf("downloaded, but not recorded: %w", err)
	}
	fmt.Fprintf(stderr, "Recorded in .hifin/%s; `hi data get` there fetches the same files again.\n", dataRecordFile)
	return nil
}

// ---------------------------------------------------------------------------
// the menu

func dataMenuLabel(item dataItem) string {
	size := formatDataSize(item.Size)
	if item.Kind == "bucket" {
		size += fmt.Sprintf(", %d files", item.Files)
	}
	return fmt.Sprintf("%-7s  %s  ·  %s  ·  updated %s", item.Kind, item.ID, size, describeDataAge(item.Updated))
}

func dataMenu(ui menuUI, stdin io.Reader, stdout, stderr io.Writer) error {
	client, connection, err := dataClient()
	if err != nil {
		return err
	}
	var catalog apiDataCatalog
	ui.busy("Asking "+connection.URL+" what you may download…", func() { catalog, err = fetchDataCatalog(client) })
	if err != nil {
		return err
	}
	for _, org := range catalog.Orgs {
		if org.Problem != "" {
			ui.note(fmt.Sprintf("%s: %s", org.Name, org.Problem))
		}
	}
	if len(catalog.Items) == 0 {
		printDataCatalog(catalog, stdout, stderr)
		return nil
	}
	labels := make([]string, len(catalog.Items))
	for i, item := range catalog.Items {
		labels[i] = dataMenuLabel(item)
	}
	choice, err := ui.choose("Which one do you want to download?", labels, true)
	if err != nil {
		return nil
	}
	item := catalog.Items[choice]
	folder, err := ui.input("Download into", defaultDataFolder(item), nil)
	if err != nil {
		return nil
	}
	card := fmt.Sprintf("%s %s\n%s into %s", item.Kind, item.ID, formatDataSize(item.Size), folder)
	if ok, err := ui.confirm("Download it?", card, false); err != nil || !ok {
		return nil
	}
	command := "hi data get " + item.ID
	if folder != defaultDataFolder(item) {
		command += " --to " + folder
	}
	ui.command(command)
	return downloadData(client, connection, item, dataGetOptions{to: folder}, stdin, stdout, stderr)
}
