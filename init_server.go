package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Server templates on a connected device: the catalog and signed bundles
// from the server, cached per commit (docs/specs/approved/hi_init.md).

var templateFetchTimeout = 15 * time.Second

func templateCacheDirectory() string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = filepath.Join(os.TempDir(), "hi-cache")
	}
	return filepath.Join(base, "hi", "templates")
}

// pinServerKey stores the server's public key at the first connection and
// refuses a different one later.
func pinServerKey(connection *serverConnection, me apiMe, stdout io.Writer) error {
	if me.ServerKey == "" {
		return nil
	}
	if connection.ServerKey == "" {
		connection.ServerKey = me.ServerKey
		if public, err := base64.StdEncoding.DecodeString(me.ServerKey); err == nil && len(public) == ed25519.PublicKeySize {
			fmt.Fprintf(stdout, "Stored the server's key %s; template bundles must be signed with it.\n", keyFingerprint(public))
		}
		return saveServerConnection(*connection)
	}
	if connection.ServerKey != me.ServerKey {
		return fmt.Errorf("the server at %s has a different key than when this device connected; "+
			"if an admin replaced the server on purpose, run `hi disconnect` and `hi connect` again", connection.URL)
	}
	return nil
}

func (c *serverConnection) serverPublicKey() (ed25519.PublicKey, error) {
	public, err := base64.StdEncoding.DecodeString(c.ServerKey)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return nil, errors.New("this device has no stored server key; run `hi connect status` once")
	}
	return ed25519.PublicKey(public), nil
}

type serverTemplateSource struct {
	name   string
	commit string
	files  fs.FS
	cached bool // the server was unreachable, so this is the last commit seen
}

// loadServerTemplateSources returns the server's sources for a connected
// device, from the cache when the server can't be reached. Problems are
// warnings: the built-in templates always work.
func loadServerTemplateSources(stderr io.Writer) []serverTemplateSource {
	connection, err := loadServerConnection()
	if err != nil || connection == nil {
		return nil
	}
	sources, err := fetchServerTemplateSources(connection)
	if err == nil {
		return sources
	}
	fmt.Fprintf(stderr, "hi: warning: no server templates from %s: %v\n", connection.URL, err)
	var reply *serverReplyError
	if errors.As(err, &reply) || strings.Contains(err.Error(), "different key") {
		return nil
	}
	return cachedServerTemplateSources()
}

func fetchServerTemplateSources(connection *serverConnection) ([]serverTemplateSource, error) {
	key, err := loadDeviceKey(false)
	if err != nil {
		return nil, err
	}
	client := newServerClient(connection.URL, key)
	client.http.Timeout = templateFetchTimeout
	if connection.ServerKey == "" {
		me, err := client.me()
		if err != nil {
			return nil, err
		}
		if err := pinServerKey(connection, me, os.Stderr); err != nil {
			return nil, err
		}
	}
	public, err := connection.serverPublicKey()
	if err != nil {
		return nil, err
	}
	var catalog apiTemplateCatalog
	if err := client.call(http.MethodGet, "/v1/templates", nil, &catalog); err != nil {
		return nil, err
	}
	var sources []serverTemplateSource
	for _, info := range catalog.Sources {
		if !validServerName(info.Name) || info.Name == "builtin" || info.Name == "local" || !validCommit(info.Commit) {
			return nil, fmt.Errorf("the server listed a malformed source %q", info.Name)
		}
		directory := filepath.Join(templateCacheDirectory(), info.Name, info.Commit)
		if _, err := os.Stat(directory); err != nil {
			var bundle apiTemplateBundle
			client.http.Timeout = time.Minute
			if err := client.call(http.MethodGet, "/v1/templates/"+info.Name+"/"+info.Commit, nil, &bundle); err != nil {
				return nil, err
			}
			if err := storeTemplateBundle(public, info.Name, info.Commit, bundle); err != nil {
				return nil, err
			}
		}
		os.WriteFile(filepath.Join(templateCacheDirectory(), info.Name, "current"), []byte(info.Commit+"\n"), 0o644)
		sources = append(sources, serverTemplateSource{name: info.Name, commit: info.Commit, files: os.DirFS(directory)})
	}
	pruneTemplateCache(catalog)
	return sources, nil
}

// pruneTemplateCache forgets sources the server no longer offers, such as a
// renamed or removed one, so they don't come back when it is unreachable.
func pruneTemplateCache(catalog apiTemplateCatalog) {
	offered := map[string]bool{}
	for _, info := range catalog.Sources {
		offered[info.Name] = true
	}
	entries, _ := os.ReadDir(templateCacheDirectory())
	for _, entry := range entries {
		if entry.IsDir() && !offered[entry.Name()] {
			os.RemoveAll(filepath.Join(templateCacheDirectory(), entry.Name()))
		}
	}
}

func validCommit(commit string) bool {
	if len(commit) < 7 || len(commit) > 64 {
		return false
	}
	for _, char := range commit {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// storeTemplateBundle checks the server's signature, then unpacks the
// archive into the cache. Nothing from a bundle that fails the check is
// kept.
func storeTemplateBundle(public ed25519.PublicKey, source, commit string, bundle apiTemplateBundle) error {
	if bundle.Source != source || bundle.Commit != commit ||
		!ed25519.Verify(public, templateBundlePayload(source, commit, bundle.Archive), bundle.Signature) {
		return fmt.Errorf("the %s templates at %s are not signed by this server's key; nothing was used", source, shortCommit(commit))
	}
	parent := filepath.Join(templateCacheDirectory(), source)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(parent, ".unpack-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	if err := unpackTemplateArchive(bundle.Archive, temporary); err != nil {
		return fmt.Errorf("unpack the %s templates: %w", source, err)
	}
	destination := filepath.Join(parent, commit)
	if err := os.Rename(temporary, destination); err != nil && !errors.Is(err, fs.ErrExist) {
		if _, statErr := os.Stat(destination); statErr != nil {
			return err
		}
	}
	return nil
}

// unpackTemplateArchive writes regular files and folders only, and refuses
// any path outside the destination.
func unpackTemplateArchive(archive []byte, destination string) error {
	compressed, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	reader := tar.NewReader(compressed)
	total := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(header.Name)
		if header.Typeflag == tar.TypeXGlobalHeader || name == "." {
			continue
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe path %q", header.Name)
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += int(header.Size)
			if total > 4*templateBundleLimit {
				return errors.New("the archive is too large")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			data, err := io.ReadAll(io.LimitReader(reader, header.Size))
			if err != nil {
				return err
			}
			if err := os.WriteFile(target, data, 0o644); err != nil {
				return err
			}
		}
	}
}

// cachedServerTemplateSources are the last commits this device fetched.
func cachedServerTemplateSources() []serverTemplateSource {
	entries, err := os.ReadDir(templateCacheDirectory())
	if err != nil {
		return nil
	}
	var sources []serverTemplateSource
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(templateCacheDirectory(), entry.Name(), "current"))
		commit := strings.TrimSpace(string(data))
		if err != nil || !validCommit(commit) {
			continue
		}
		directory := filepath.Join(templateCacheDirectory(), entry.Name(), commit)
		if _, err := os.Stat(directory); err == nil {
			sources = append(sources, serverTemplateSource{name: entry.Name(), commit: commit, files: os.DirFS(directory), cached: true})
		}
	}
	return sources
}

// reportTemplateUse tells the server a project was made from one of its
// templates: the template's name, nothing else.
func reportTemplateUse(template string) {
	connection, err := loadServerConnection()
	if err != nil || connection == nil {
		return
	}
	key, err := loadDeviceKey(false)
	if err != nil {
		return
	}
	client := newServerClient(connection.URL, key)
	client.http.Timeout = 5 * time.Second
	client.call(http.MethodPost, "/v1/activity", apiActivity{Command: "init", Instance: template}, nil)
}
