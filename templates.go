package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// The built-in templates are public, generic project layouts. Anything
// private comes from a server source instead (docs/specs/approved/hi_init.md).
//
//go:embed all:templates
var builtinTemplateFiles embed.FS

const (
	layerManifestName = "layer.json"
	fragmentSuffix    = ".fragment"
)

// templateLayer is one layer's layer.json plus where its files live.
type templateLayer struct {
	Name       string                   `json:"name"`
	Summary    string                   `json:"summary"`
	Extends    string                   `json:"extends"`
	Hidden     bool                     `json:"hidden"`
	Schema     int                      `json:"schema"`
	RequiresHi string                   `json:"requires_hi"`
	Params     map[string]templateParam `json:"params"`
	Substitute map[string]string        `json:"substitute"`
	Files      templateFileClasses      `json:"files"`
	Skills     []string                 `json:"skills"`
	Commands   map[string][][]string    `json:"commands"`
	// Remove drops files that earlier layers wrote, such as their examples.
	Remove []string `json:"remove"`
	source string
	files  fs.FS
	// key is the name to choose it by: the layer's name, or source/name
	// when two server sources use the same name.
	key string
	// commit is a server source's commit; cached marks one read from the
	// cache because the server was unreachable.
	commit string
	cached bool
}

type templateParam struct {
	Pattern string `json:"pattern"`
}

type templateFileClasses struct {
	Owned   []string `json:"owned"`
	Managed []string `json:"managed"`
}

// composedTemplate is a template's layers applied in order, ready to write.
type composedTemplate struct {
	Name     string
	Layers   []*templateLayer
	Files    map[string][]byte
	Owned    []string
	Managed  []string
	Skills   []string
	Commands map[string][][]string
}

// loadTemplateSource reads every layer in a source: each top-level folder
// with a layer.json.
func loadTemplateSource(source string, files fs.FS) (map[string]*templateLayer, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	layers := map[string]*templateLayer{}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "skills" {
			continue
		}
		data, err := fs.ReadFile(files, path.Join(entry.Name(), layerManifestName))
		if err != nil {
			continue
		}
		layer := &templateLayer{}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(layer); err != nil {
			return nil, fmt.Errorf("%s: %s/%s: %w", source, entry.Name(), layerManifestName, err)
		}
		if layer.Name != entry.Name() {
			return nil, fmt.Errorf("%s: %s/%s names the layer %q", source, entry.Name(), layerManifestName, layer.Name)
		}
		layer.source, layer.key = source, layer.Name
		layer.files, err = fs.Sub(files, entry.Name())
		if err != nil {
			return nil, err
		}
		layers[layer.Name] = layer
	}
	return layers, nil
}

func builtinTemplateLayers() (map[string]*templateLayer, error) {
	files, err := fs.Sub(builtinTemplateFiles, "templates")
	if err != nil {
		return nil, err
	}
	return loadTemplateSource("builtin", files)
}

// layerChain returns the layers of a template from the root (base) down.
func layerChain(layers map[string]*templateLayer, name string) ([]*templateLayer, error) {
	var chain []*templateLayer
	seen := map[string]bool{}
	for current := name; current != ""; {
		layer, ok := layers[current]
		if !ok {
			if current == name {
				return nil, fmt.Errorf("no template named %q", name)
			}
			return nil, fmt.Errorf("template %q extends %q, which does not exist", chain[len(chain)-1].Name, current)
		}
		if seen[current] {
			return nil, fmt.Errorf("template %q extends itself through %q", name, current)
		}
		seen[current] = true
		chain = append([]*templateLayer{layer}, chain...)
		// A parent in the layer's own source wins over one with the same
		// name elsewhere.
		current = layer.Extends
		if _, ok := layers[layer.source+"/"+current]; ok && current != "" {
			current = layer.source + "/" + current
		}
	}
	return chain, nil
}

// composeTemplate applies a template's layers in order with the given
// parameters. A later layer's file replaces an earlier one's; a file named
// <path>.fragment is added to <path> inside its managed block.
func composeTemplate(layers map[string]*templateLayer, name string, params map[string]string) (*composedTemplate, error) {
	chain, err := layerChain(layers, name)
	if err != nil {
		return nil, err
	}
	if chain[len(chain)-1].Hidden {
		return nil, fmt.Errorf("%q is a shared layer, not a template", name)
	}
	result := &composedTemplate{Name: name, Layers: chain, Files: map[string][]byte{}, Commands: map[string][][]string{}}
	substitute := map[string]string{}
	for _, layer := range chain {
		for key, param := range layer.Params {
			if param.Pattern == "" {
				continue
			}
			pattern, err := regexp.Compile(param.Pattern)
			if err != nil {
				return nil, fmt.Errorf("%s: parameter %s: %w", layer.Name, key, err)
			}
			if !pattern.MatchString(params[key]) {
				return nil, fmt.Errorf("%s %q must match %s", key, params[key], param.Pattern)
			}
		}
		for sentinel, value := range layer.Substitute {
			substitute[sentinel] = expandTemplateValue(value, params)
		}
		result.Owned = append(result.Owned, layer.Files.Owned...)
		result.Managed = append(result.Managed, layer.Files.Managed...)
		result.Skills = append(result.Skills, layer.Skills...)
		for command, args := range layer.Commands {
			result.Commands[command] = args
		}
		for _, removed := range layer.Remove {
			if _, ok := result.Files[removed]; !ok {
				return nil, fmt.Errorf("%s removes %s, which no earlier layer writes", layer.Name, removed)
			}
			delete(result.Files, removed)
		}
		err := fs.WalkDir(layer.files, ".", func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if skipTemplateDirectory(entry.Name()) && name != "." {
					return fs.SkipDir
				}
				return nil
			}
			if name == layerManifestName {
				return nil
			}
			data, err := fs.ReadFile(layer.files, name)
			if err != nil {
				return err
			}
			if target, ok := strings.CutSuffix(name, fragmentSuffix); ok {
				result.Files[target] = insertFragment(result.Files[target], data)
			} else {
				result.Files[name] = data
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", layer.Name, err)
		}
	}

	// Substitute last, so fragments and replaced files are covered too.
	sentinels := make([]string, 0, len(substitute))
	for sentinel := range substitute {
		sentinels = append(sentinels, sentinel)
	}
	sort.Slice(sentinels, func(i, j int) bool { return len(sentinels[i]) > len(sentinels[j]) })
	pairs := make([]string, 0, 2*len(sentinels))
	for _, sentinel := range sentinels {
		pairs = append(pairs, sentinel, substitute[sentinel])
	}
	replacer := strings.NewReplacer(pairs...)
	files := make(map[string][]byte, len(result.Files))
	for name, data := range result.Files {
		if isTextFile(data) {
			data = []byte(replacer.Replace(string(data)))
		}
		files[replacer.Replace(name)] = data
	}
	result.Files = files
	result.Skills = uniqueStrings(result.Skills)
	return result, nil
}

// skipTemplateDirectory leaves out what a template author's own setup run
// leaves behind in a layer.
func skipTemplateDirectory(name string) bool {
	switch name {
	case "node_modules", ".venv", "dist", "__pycache__", ".pytest_cache", ".ruff_cache":
		return true
	}
	return false
}

// insertFragment adds a fragment before the managed block's end marker, or
// at the end when the file has no managed block.
func insertFragment(existing, fragment []byte) []byte {
	text := string(existing)
	lines := strings.SplitAfter(text, "\n")
	for i, line := range lines {
		if strings.Contains(line, "hi:end") {
			before := strings.Join(lines[:i], "")
			return []byte(before + string(fragment) + strings.Join(lines[i:], ""))
		}
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text + string(fragment))
}

func expandTemplateValue(value string, params map[string]string) string {
	name := params["name"]
	return strings.NewReplacer(
		"{{name}}", name,
		"{{name_snake}}", strings.ReplaceAll(name, "-", "_"),
		"{{name_title}}", titleName(name),
	).Replace(value)
}

// titleName turns "pricing-tools" into "Pricing Tools".
func titleName(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' })
	for i, word := range words {
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

func isTextFile(data []byte) bool {
	return utf8.Valid(data) && !bytes.ContainsRune(data, 0)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var unique []string
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			unique = append(unique, value)
		}
	}
	return unique
}
