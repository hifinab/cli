package main

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// hi compute run --data: the job downloads the team's data before the
// script runs. A rented instance can't reach the hi server, so hi asks
// the server for signed links when the run starts and ships them inside
// a wrapper script; the instance never holds a token.

// dataRunMaxScript is how large a wrapped script may be for Hugging Face
// Jobs, which carry it in one environment variable (Linux allows 128 KiB).
const dataRunMaxScript = 120 << 10

// dataRunRef is one --data value: [kind:]<org>/<name>[/<pattern>].
type dataRunRef struct {
	ref     string // what resolveDataItem takes
	id      string // <org>/<name>
	include string
}

func parseDataRunRef(value string) (dataRunRef, error) {
	kind, rest := "", value
	if before, after, found := strings.Cut(value, ":"); found && containsString(dataKinds, before) {
		kind, rest = before+":", after
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return dataRunRef{}, usageError{fmt.Sprintf("--data %q: use <org>/<name>, optionally with a folder or pattern after it, as in hifinab/fdb/runs/*", value)}
	}
	id := parts[0] + "/" + parts[1]
	ref := dataRunRef{ref: kind + id, id: id}
	if len(parts) == 3 && parts[2] != "" {
		ref.include = parts[2]
		if !strings.ContainsAny(ref.include, "*?") && !strings.Contains(filepath.Base(ref.include), ".") {
			ref.include = strings.TrimSuffix(ref.include, "/") + "/*" // a folder
		}
	}
	return ref, nil
}

// dataRunRepo is one repository in the wrapper's manifest.
type dataRunRepo struct {
	ID    string        `json:"id"`
	To    string        `json:"to"`
	Files []apiDataLink `json:"files"`
}

// fetchDataRunLinks asks the server for the links of every --data value.
func fetchDataRunLinks(refs []dataRunRef, stderr io.Writer) ([]dataRunRepo, error) {
	client, _, err := dataClient()
	if err != nil {
		return nil, fmt.Errorf("--data: %w", err)
	}
	client.http.Timeout = 0 // resolving many files takes a while
	catalog, err := fetchDataCatalog(client)
	if err != nil {
		return nil, err
	}
	var repos []dataRunRepo
	for _, ref := range refs {
		item, err := resolveDataItem(catalog, ref.ref)
		if err != nil {
			return nil, err
		}
		request := apiDataLinksRequest{Scope: item.Kind + ":" + item.ID}
		if ref.include != "" {
			request.Include = []string{ref.include}
		}
		fmt.Fprintf(stderr, "Asking the hi server for download links to %s %s…\n", item.Kind, item.ID)
		var links apiDataLinks
		if err := client.call(http.MethodPost, "/v1/data/links", request, &links); err != nil {
			return nil, err
		}
		var size int64
		for _, file := range links.Files {
			size += file.Size
		}
		at := ""
		if links.Revision != "" {
			at = " at " + shortCommit(links.Revision)
		}
		fmt.Fprintf(stderr, "  %d files, %s%s, into %s on the instance\n", len(links.Files), formatDataSize(size), at, defaultDataFolder(item))
		repos = append(repos, dataRunRepo{ID: item.ID, To: filepath.ToSlash(defaultDataFolder(item)), Files: links.Files})
	}
	return repos, nil
}

var pep723Block = regexp.MustCompile(`(?ms)^# /// script\s*$.*?^# ///\s*$`)

// dataRunWrapper is a Python script that downloads the manifest's files
// and then runs the user's script as __main__, with its arguments. The
// script's inline metadata (PEP 723) is kept at the top, so uv installs
// its dependencies as before.
func dataRunWrapper(scriptName string, script []byte, repos []dataRunRepo) ([]byte, error) {
	// Small files kept in git travel as raw bytes after the manifest, so
	// they compress well; entries point at them by offset.
	type packedFile struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		URL    string `json:"url,omitempty"`
		Offset int    `json:"offset"`
		Length int    `json:"length"`
		Inline bool   `json:"inline,omitempty"`
	}
	type packedRepo struct {
		ID    string       `json:"id"`
		To    string       `json:"to"`
		Files []packedFile `json:"files"`
	}
	var blobs bytes.Buffer
	var index []packedRepo
	for _, repo := range repos {
		entry := packedRepo{ID: repo.ID, To: repo.To}
		for _, file := range repo.Files {
			packed := packedFile{Path: file.Path, Size: file.Size, URL: file.URL}
			if file.URL == "" {
				packed.Inline, packed.Offset, packed.Length = true, blobs.Len(), len(file.Content)
				blobs.Write(file.Content)
			}
			entry.Files = append(entry.Files, packed)
		}
		index = append(index, entry)
	}
	manifest, err := json.Marshal(index)
	if err != nil {
		return nil, err
	}
	var packed bytes.Buffer
	writer, _ := zlib.NewWriterLevel(&packed, zlib.BestCompression)
	fmt.Fprintf(writer, "%010d", len(manifest))
	writer.Write(manifest)
	writer.Write(blobs.Bytes())
	writer.Close()
	var out strings.Builder
	if block := pep723Block.Find(script); block != nil {
		out.Write(block)
		out.WriteString("\n")
	}
	fmt.Fprintf(&out, dataRunWrapperSource,
		base64.StdEncoding.EncodeToString(packed.Bytes()),
		base64.StdEncoding.EncodeToString(script),
		scriptName)
	return []byte(out.String()), nil
}

const dataRunWrapperSource = `# Written by hi compute run --data: download the team's data, then run
# the script. The links are signed, last about an hour, and need no token.
import base64, json, os, sys, time, zlib, runpy, tempfile, urllib.error, urllib.request
from concurrent.futures import ThreadPoolExecutor

_PACKED = zlib.decompress(base64.b64decode("%s"))
_MANIFEST = json.loads(_PACKED[10:10 + int(_PACKED[:10])])
_BLOBS = _PACKED[10 + int(_PACKED[:10]):]
_SCRIPT = base64.b64decode("%s")
_NAME = %q


def _fetch(target, entry):
    if os.path.exists(target) and os.path.getsize(target) == entry["size"]:
        return 0
    os.makedirs(os.path.dirname(target) or ".", exist_ok=True)
    if entry.get("inline"):
        data = _BLOBS[entry["offset"]:entry["offset"] + entry["length"]]
        with open(target, "wb") as f:
            f.write(data)
        return len(data)
    partial = target + ".hi-partial"
    last = None
    for attempt in range(4):
        try:
            with urllib.request.urlopen(entry["url"], timeout=120) as response, open(partial, "wb") as f:
                while True:
                    chunk = response.read(1 << 20)
                    if not chunk:
                        break
                    f.write(chunk)
            if os.path.getsize(partial) != entry["size"]:
                raise IOError("got %%d bytes of %%d" %% (os.path.getsize(partial), entry["size"]))
            os.replace(partial, target)
            return entry["size"]
        except urllib.error.HTTPError as error:
            if error.code in (401, 403):
                raise SystemExit("hi data: the download link for %%s has expired (they last about an hour); run hi compute run again" %% entry["path"])
            last = error
        except Exception as error:
            last = error
        time.sleep(2 ** attempt)
    raise SystemExit("hi data: could not download %%s: %%s" %% (entry["path"], last))


def _download():
    started = time.time()
    for repo in _MANIFEST:
        total = sum(entry["size"] for entry in repo["files"])
        print("hi data: %%s: %%d files, %%.1f MB into %%s" %% (repo["id"], len(repo["files"]), total / 1e6, repo["to"]), flush=True)
        with ThreadPoolExecutor(max_workers=8) as pool:
            jobs = [pool.submit(_fetch, os.path.join(repo["to"], entry["path"]), entry) for entry in repo["files"]]
            for job in jobs:
                job.result()
    print("hi data: ready in %%.0f s" %% (time.time() - started), flush=True)


_download()
_path = os.path.join(tempfile.mkdtemp(prefix="hi-run-"), _NAME)
with open(_path, "wb") as _f:
    _f.write(_SCRIPT)
sys.argv[0] = _path
runpy.run_path(_path, run_name="__main__")
`

// dataRunTooLarge explains what made the job too large for Hugging Face.
func dataRunTooLarge(repos []dataRunRepo, size int) error {
	var inline []string
	links := 0
	for _, repo := range repos {
		for _, file := range repo.Files {
			if file.URL == "" && len(file.Content) > 16<<10 {
				inline = append(inline, fmt.Sprintf("%s/%s (%s)", repo.ID, file.Path, formatDataSize(int64(len(file.Content)))))
			}
			if file.URL != "" {
				links++
			}
		}
	}
	message := fmt.Sprintf("the data is too much to send with one Hugging Face job (%d KB of %d KB)", size>>10, dataRunMaxScript>>10)
	if len(inline) > 0 {
		message += "; these files are kept in git rather than Hugging Face's storage, so they have no download link and travel inside the job: " +
			strings.Join(inline, ", ") + ". Leave them out with a pattern, or run on Colab, which has no such limit"
	} else {
		message += fmt.Sprintf("; %d download links is too many, so name a folder or pattern, as in --data hifinab/fdb/runs/2026-09/*, or run on Colab", links)
	}
	return errors.New(message)
}

// prepareDataRun replaces the run's script with a wrapper that downloads
// the data first. It returns a cleanup for the temporary file.
func prepareDataRun(request *runRequest, values []string, provider string, stderr io.Writer) (func(), error) {
	if request.script == "" {
		return nil, usageError{"--data works with script runs (hi compute run --data <org>/<name> script.py)"}
	}
	var refs []dataRunRef
	for _, value := range values {
		ref, err := parseDataRunRef(value)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	script, err := os.ReadFile(request.script)
	if err != nil {
		return nil, err
	}
	repos, err := fetchDataRunLinks(refs, stderr)
	if err != nil {
		return nil, err
	}
	wrapper, err := dataRunWrapper(filepath.Base(request.script), script, repos)
	if err != nil {
		return nil, err
	}
	if size := base64.StdEncoding.EncodedLen(len(wrapper)); provider == "hf" && size > dataRunMaxScript {
		return nil, dataRunTooLarge(repos, size)
	}
	dir, err := os.MkdirTemp("", "hi-run-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, filepath.Base(request.script))
	if err := os.WriteFile(path, wrapper, 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	request.script = path
	return func() { os.RemoveAll(dir) }, nil
}
