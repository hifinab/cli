package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// hi compute run --data through hi server expose: on Hugging Face Jobs, the
// job reaches the server's data proxy at a public URL with a run token,
// sent as an encrypted job secret. No size limit and no one-hour links;
// the script can also call load_dataset for the named repositories.
// Without exposure, signed links are used instead.

const dataRunTokenSecret = "HI_DATA_TOKEN"

// dataRunProxyRepo is one repository the wrapper downloads through the
// proxy, at a pinned commit.
type dataRunProxyRepo struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Revision string `json:"revision,omitempty"`
	To       string `json:"to"`
	Include  string `json:"include,omitempty"`
}

// errNoRunAccess means the server can't expose; signed links still work.
var errNoRunAccess = errors.New("no run access")

// requestRunAccess asks the server for a run token for the --data
// repositories; with pin, it also pins datasets and models to their
// current commit. It returns errNoRunAccess when the server can't expose.
func requestRunAccess(refs []dataRunRef, seconds int64, run string, pin bool) ([]dataRunProxyRepo, apiRunAccess, error) {
	var access apiRunAccess
	client, connection, err := dataClient()
	if err != nil {
		return nil, access, fmt.Errorf("--data: %w", err)
	}
	client.http.Timeout = 0 // starting netbird expose takes a few seconds
	catalog, err := fetchDataCatalog(client)
	if err != nil {
		return nil, access, err
	}
	var repos []dataRunProxyRepo
	var scopes []string
	for _, ref := range refs {
		item, err := resolveDataItem(catalog, ref.ref)
		if err != nil {
			return nil, access, err
		}
		repo := dataRunProxyRepo{Kind: item.Kind, ID: item.ID, To: defaultDataFolder(item), Include: ref.include}
		if pin && item.Kind != "bucket" {
			token, endpoint, err := dataToken(client, connection, item.Kind+":"+item.ID)
			if err != nil {
				return nil, access, err
			}
			if repo.Revision, err = dataResolveCommit(endpoint, token, item, ""); err != nil {
				return nil, access, err
			}
		}
		repos = append(repos, repo)
		scopes = append(scopes, item.Kind+":"+item.ID)
	}
	err = client.call(http.MethodPost, "/v1/data/run-access", map[string]any{"scopes": scopes, "seconds": seconds, "run": run}, &access)
	var reply *serverReplyError
	if errors.As(err, &reply) && (reply.status == http.StatusNotFound || reply.status == http.StatusServiceUnavailable) {
		return nil, access, fmt.Errorf("%w: %s", errNoRunAccess, reply.message)
	}
	return repos, access, err
}

// dataRunProxyWrapper downloads through the proxy and then runs the script
// with HF_ENDPOINT and HF_TOKEN set, so it can read the repositories too.
func dataRunProxyWrapper(scriptName string, script []byte, repos []dataRunProxyRepo) ([]byte, error) {
	manifest, err := json.Marshal(repos)
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	if block := pep723Block.Find(script); block != nil {
		out.Write(block)
		out.WriteString("\n")
	}
	fmt.Fprintf(&out, dataRunProxySource, base64.StdEncoding.EncodeToString(manifest),
		base64.StdEncoding.EncodeToString(script), scriptName, dataRunTokenSecret)
	return []byte(out.String()), nil
}

const dataRunProxySource = `# Written by hi compute run --data: download the team's data through the
# hi server, then run the script. The run token reads only these
# repositories, until the run's time limit.
import base64, fnmatch, json, os, re, sys, time, runpy, tempfile, urllib.error, urllib.parse, urllib.request
from concurrent.futures import ThreadPoolExecutor

_REPOS = json.loads(base64.b64decode("%s"))
_SCRIPT = base64.b64decode("%s")
_NAME = %q
_ENDPOINT = os.environ["HI_DATA_ENDPOINT"].rstrip("/")
_TOKEN = os.environ.pop(%q)


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


_opener = urllib.request.build_opener(_NoRedirect)


def _call(url, method="GET", auth=True):
    request = urllib.request.Request(url, method=method)
    if auth:
        request.add_header("Authorization", "Bearer " + _TOKEN)
    request.add_header("User-Agent", "hi-compute-run")
    try:
        return _opener.open(request, timeout=120)
    except urllib.error.HTTPError as error:
        if 300 <= error.code < 400:
            return error
        detail = error.read().decode(errors="replace")[:300]
        raise SystemExit("hi data: %%s answered %%d: %%s" %% (url.split("?")[0], error.code, detail))


def _quote(path):
    return "/".join(urllib.parse.quote(part, safe="") for part in path.split("/"))


def _tree(repo):
    if repo["kind"] == "bucket":
        url = "%%s/api/buckets/%%s/tree?recursive=true" %% (_ENDPOINT, repo["id"])
    else:
        url = "%%s/api/%%ss/%%s/tree/%%s?recursive=true" %% (_ENDPOINT, repo["kind"], repo["id"], repo["revision"])
    files = []
    while url:
        response = _call(url)
        files += [entry for entry in json.load(response) if entry.get("type") == "file"]
        match = re.search(r'<([^>]+)>;\s*rel="next"', response.headers.get("Link", ""))
        url = match.group(1) if match else None
    if repo.get("include"):
        files = [entry for entry in files if fnmatch.fnmatch(entry["path"], repo["include"])]
    return files


def _resolve(repo, path):
    if repo["kind"] == "bucket":
        return "%%s/buckets/%%s/resolve/%%s" %% (_ENDPOINT, repo["id"], _quote(path))
    prefix = "/datasets/" if repo["kind"] == "dataset" else "/"
    return "%%s%%s%%s/resolve/%%s/%%s" %% (_ENDPOINT, prefix, repo["id"], repo["revision"], _quote(path))


def _fetch(repo, entry):
    target = os.path.join(repo["to"], entry["path"])
    if os.path.exists(target) and os.path.getsize(target) == entry["size"]:
        return
    os.makedirs(os.path.dirname(target) or ".", exist_ok=True)
    partial, last = target + ".hi-partial", None
    for attempt in range(4):
        try:
            url, auth = _resolve(repo, entry["path"]), True
            for _ in range(5):
                response = _call(url, auth=auth)
                if 300 <= response.status < 400:
                    location = urllib.parse.urljoin(url, response.headers["Location"])
                    # Links within the proxy keep the token; the CDN's are signed.
                    auth = location.startswith(_ENDPOINT + "/")
                    url = location
                    continue
                break
            with response, open(partial, "wb") as f:
                while True:
                    chunk = response.read(1 << 20)
                    if not chunk:
                        break
                    f.write(chunk)
            if os.path.getsize(partial) != entry["size"]:
                raise IOError("got %%d bytes of %%d" %% (os.path.getsize(partial), entry["size"]))
            os.replace(partial, target)
            return
        except SystemExit:
            raise
        except Exception as error:
            last = error
            time.sleep(2 ** attempt)
    raise SystemExit("hi data: could not download %%s: %%s" %% (entry["path"], last))


def _download():
    started = time.time()
    for repo in _REPOS:
        files = _tree(repo)
        total = sum(entry["size"] for entry in files)
        print("hi data: %%s: %%d file%%s, %%.1f MB into %%s" %% (repo["id"], len(files), "" if len(files) == 1 else "s", total / 1e6, repo["to"]), flush=True)
        with ThreadPoolExecutor(max_workers=8) as pool:
            for job in [pool.submit(_fetch, repo, entry) for entry in files]:
                job.result()
    print("hi data: ready in %%.0f s" %% (time.time() - started), flush=True)


_download()
os.environ["HF_ENDPOINT"], os.environ["HF_TOKEN"] = _ENDPOINT, _TOKEN
_path = os.path.join(tempfile.mkdtemp(prefix="hi-run-"), _NAME)
with open(_path, "wb") as _f:
    _f.write(_SCRIPT)
sys.argv[0] = _path
runpy.run_path(_path, run_name="__main__")
`

// prepareDataRunProxy sets up a Hugging Face job to download through the
// exposed proxy. It returns errNoRunAccess when the server can't expose.
func prepareDataRunProxy(request *runRequest, refs []dataRunRef, script []byte, stderr io.Writer) ([]byte, error) {
	seconds := int64(0)
	if request.max > 0 && request.max != noLimit {
		seconds = int64(request.max.Seconds())
	}
	repos, access, err := requestRunAccess(refs, seconds, request.name, true)
	if errors.Is(err, errNoRunAccess) {
		fmt.Fprintf(stderr, "hi: the server can't expose the data proxy (%s); using signed links instead\n",
			strings.TrimPrefix(err.Error(), errNoRunAccess.Error()+": "))
	}
	if err != nil {
		return nil, err
	}
	for _, repo := range repos {
		at := ""
		if repo.Revision != "" {
			at = " at " + shortCommit(repo.Revision)
		}
		fmt.Fprintf(stderr, "  %s %s%s into %s on the instance\n", repo.Kind, repo.ID, at, repo.To)
	}
	fmt.Fprintf(stderr, "The job reaches the data through %s with a run token until %s.\n",
		strings.TrimSuffix(access.Endpoint, dataProxyPath), access.Expires.Local().Format("2006-01-02 15:04"))
	wrapper, err := dataRunProxyWrapper(filepath.Base(request.script), script, repos)
	if err != nil {
		return nil, err
	}
	// The token goes as an encrypted job secret, read from this process's
	// environment by name; the endpoint is not secret.
	os.Setenv(dataRunTokenSecret, access.Token)
	request.secrets = append(request.secrets, dataRunTokenSecret)
	request.env = append(request.env, "HI_DATA_ENDPOINT="+access.Endpoint)
	return wrapper, nil
}
