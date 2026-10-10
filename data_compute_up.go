package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// hi compute up --data: a RunPod or Shadeform machine reads the team's
// data through the hi server's exposed data proxy, with a run token for the
// named repositories and the machine's lifetime. hi writes HF_ENDPOINT and
// HF_TOKEN into a file on the machine that its shells load, over SSH, so
// the token never appears in the provider's console or API.

// dataUpProviders are the providers whose machines hi can reach over SSH
// to hand them the token.
var dataUpProviders = map[string]bool{"runpod": true, "shadeform": true}

// dataUpFile is where the machine keeps the endpoint and token.
const dataUpFile = "~/.hi/data.env"

// checkDataUp parses up's --data values and refuses them where the token
// can't go safely.
func checkDataUp(provider computeProvider, request upRequest, values []string) ([]dataRunRef, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if !dataUpProviders[provider.name()] {
		return nil, usageError{"--data on hi compute up works on RunPod and Shadeform; on Hugging Face, use hi compute run --data"}
	}
	if isCommunityHardware(request.hardware.name) {
		return nil, usageError{"--data puts a token for your team's data on the machine, and Community Cloud machines are third-party hosts; choose Secure Cloud hardware"}
	}
	if request.noWait {
		return nil, usageError{"--data needs the machine to be up to hand it the token; leave out --no-wait"}
	}
	var refs []dataRunRef
	for _, value := range values {
		ref, err := parseDataRunRef(value)
		if err != nil {
			return nil, err
		}
		if ref.include != "" {
			return nil, usageError{fmt.Sprintf("--data %q: on hi compute up, name whole repositories as <org>/<name>; download parts of them on the machine", value)}
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// requestDataUp asks the server for the machine's run token. It runs
// before the machine is created, so a refusal costs nothing.
func requestDataUp(request upRequest) (apiRunAccess, error) {
	seconds := int64(0)
	if request.max != noLimit {
		seconds = int64(request.max.Seconds())
	}
	_, access, err := requestRunAccess(request.data, seconds, request.name, false)
	if errors.Is(err, errNoRunAccess) {
		return access, fmt.Errorf("--data: the hi server can't put its data proxy where cloud machines reach it (%s); an admin can check with hi server expose",
			strings.TrimPrefix(err.Error(), errNoRunAccess.Error()+": "))
	}
	return access, err
}

// installDataUp writes the endpoint and token on the machine, and makes
// login and non-interactive shells load them.
func installDataUp(provider computeProvider, request upRequest, access apiRunAccess, stdout io.Writer) error {
	target, err := provider.ssh(request.name)
	if err != nil {
		return err
	}
	repos := dataRefIDs(request.data)
	env := fmt.Sprintf("# Written by hi compute up --data: your team's data through the hi server,\n"+
		"# for %s, until %s.\nexport HF_ENDPOINT=%s\nexport HF_TOKEN=%s\nexport HI_DATA_REPOS=%s\n",
		repos, access.Expires.UTC().Format(time.RFC3339),
		shellQuote(access.Endpoint), shellQuote(access.Token), shellQuote(strings.ReplaceAll(repos, ", ", " ")))
	// The line goes first in .bashrc, before the usual early return for
	// non-interactive shells, so hi compute ssh <name> -- <command> has it.
	command := `set -e; mkdir -p ~/.hi; umask 077; cat > ` + dataUpFile + `
line='[ -f ` + dataUpFile + ` ] && . ` + dataUpFile + `'
for f in ~/.bashrc ~/.profile; do
  touch "$f"
  grep -qxF "$line" "$f" || { printf '%s\n' "$line" | cat - "$f" > "$f.hi" && cat "$f.hi" > "$f" && rm -f "$f.hi"; }
done`
	if _, err := remoteOutput(target, strings.NewReader(env), 2*time.Minute, command); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "\n%s can read %s through the hi server until %s:\n"+
		"its shells have HF_ENDPOINT and HF_TOKEN, so hf download and load_dataset work there.\n",
		request.name, repos, access.Expires.Local().Format("2006-01-02 15:04"))
	return nil
}

func dataRefIDs(refs []dataRunRef) string {
	var ids []string
	for _, ref := range refs {
		ids = append(ids, ref.id)
	}
	return strings.Join(ids, ", ")
}
