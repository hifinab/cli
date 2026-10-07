package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// skills.sh, for finding skills and their security audits. Its documented
// API needs a Vercel project's token, so hi uses the two open endpoints npx
// skills uses. Both are optional: installing and updating need only git.

// errSkillLookupsOff means DO_NOT_TRACK or DISABLE_TELEMETRY is set.
var errSkillLookupsOff = errors.New("searches and audits on skills.sh are off (DO_NOT_TRACK)")

func skillLookupsOff() bool {
	return os.Getenv("DO_NOT_TRACK") != "" || os.Getenv("DISABLE_TELEMETRY") != ""
}

// skillsShURL is skills.sh; tests point it at a local server.
func skillsShURL() string { return firstNonEmpty(os.Getenv("HI_SKILLS_SH_URL"), "https://skills.sh") }

func skillsShAuditURL() string {
	return firstNonEmpty(os.Getenv("HI_SKILLS_SH_URL"), "https://www.skills.sh") + "/tele/audit"
}

func skillRawURL() string {
	return firstNonEmpty(os.Getenv("HI_SKILL_RAW_URL"), "https://raw.githubusercontent.com")
}

var skillHTTP = &http.Client{Timeout: 10 * time.Second}

// skillHit is one search result.
type skillHit struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	SkillID  string `json:"skillId"`
	Name     string `json:"name"`
	Installs int    `json:"installs"`
}

func (h skillHit) verified() bool { return skillVerifiedOwner(h.Source) }

// searchSkillsSh searches skills.sh, most installed first, as npx skills
// sorts them; find marks the tools' makers.
func searchSkillsSh(query string, limit int) ([]skillHit, error) {
	if skillLookupsOff() {
		return nil, errSkillLookupsOff
	}
	if len(strings.TrimSpace(query)) < 2 {
		return nil, usageError{"search for at least two characters"}
	}
	params := url.Values{"q": {query}, "limit": {fmt.Sprint(limit)}}
	response, err := skillHTTP.Get(skillsShURL() + "/api/search?" + params.Encode())
	if err != nil {
		return nil, fmt.Errorf("skills.sh can't be reached: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("skills.sh answered %s", response.Status)
	}
	var reply struct {
		Skills []skillHit `json:"skills"`
	}
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		return nil, fmt.Errorf("skills.sh answered something hi doesn't understand: %v", err)
	}
	hits := reply.Skills[:0]
	for _, hit := range reply.Skills {
		if skillGitHubShort.MatchString(hit.Source) && skillNamePattern.MatchString(firstNonEmpty(hit.SkillID, hit.Name)) {
			hits = append(hits, hit)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Installs > hits[j].Installs })
	return hits, nil
}

// skillAudit is one partner's audit of one skill.
type skillAudit struct {
	Risk       string `json:"risk"`
	Alerts     int    `json:"alerts"`
	AnalyzedAt string `json:"analyzedAt"`
}

// skillAuditPartners names the partners in the order hi shows them.
var skillAuditPartners = []struct{ key, name string }{
	{"ath", "Agent Trust Hub"}, {"socket", "Socket"}, {"snyk", "Snyk"}, {"runlayer", "Runlayer"}, {"zeroleaks", "ZeroLeaks"},
}

// fetchSkillAudits is each named skill's audits from skills.sh, for a
// GitHub source; skills without an audit are missing from the map.
func fetchSkillAudits(source string, names []string) (map[string]map[string]skillAudit, error) {
	if skillLookupsOff() {
		return nil, errSkillLookupsOff
	}
	if len(names) == 0 {
		return nil, nil
	}
	params := url.Values{"source": {source}, "skills": {strings.Join(names, ",")}}
	response, err := skillHTTP.Get(skillsShAuditURL() + "?" + params.Encode())
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("skills.sh answered %s", response.Status)
	}
	audits := map[string]map[string]skillAudit{}
	return audits, json.NewDecoder(response.Body).Decode(&audits)
}

// fetchSkillAuditsBySource looks up audits for several sources at once.
func fetchSkillAuditsBySource(names map[string][]string) map[string]map[string]map[string]skillAudit {
	results := map[string]map[string]map[string]skillAudit{}
	var lock sync.Mutex
	var group sync.WaitGroup
	for source, skills := range names {
		group.Add(1)
		go func(source string, skills []string) {
			defer group.Done()
			audits, err := fetchSkillAudits(source, skills)
			if err != nil {
				return
			}
			lock.Lock()
			results[source] = audits
			lock.Unlock()
		}(source, skills)
	}
	group.Wait()
	return results
}

// skillAuditSummary is one line: each partner's verdict, and when.
func skillAuditSummary(audits map[string]skillAudit) string {
	if len(audits) == 0 {
		return "no audit yet"
	}
	var parts []string
	latest := ""
	for _, partner := range skillAuditPartners {
		audit, ok := audits[partner.key]
		if !ok {
			continue
		}
		part := partner.name + " " + audit.Risk
		if audit.Alerts > 0 {
			part += " (" + plural(audit.Alerts, "alert") + ")"
		}
		parts = append(parts, part)
		if audit.AnalyzedAt > latest {
			latest = audit.AnalyzedAt
		}
	}
	if len(parts) == 0 {
		return "no audit yet"
	}
	if len(latest) >= 10 {
		return strings.Join(parts, " · ") + " (" + latest[:10] + ")"
	}
	return strings.Join(parts, " · ")
}

// skillAuditShort is the verdicts alone, for tables.
func skillAuditShort(audits map[string]skillAudit) string {
	var parts []string
	for _, partner := range skillAuditPartners {
		if audit, ok := audits[partner.key]; ok {
			parts = append(parts, audit.Risk)
		}
	}
	if len(parts) == 0 {
		return "no audit"
	}
	return strings.Join(parts, " ")
}

// skillRisks are the partners that rate a skill high or critical.
func skillRisks(audits map[string]skillAudit) []string {
	var risks []string
	for _, partner := range skillAuditPartners {
		audit, ok := audits[partner.key]
		if ok && (audit.Risk == "high" || audit.Risk == "critical") {
			risks = append(risks, partner.name+" "+audit.Risk)
		}
	}
	return risks
}

// skillVerifiedOwners are owners whose skills are about their own tools.
// Many repositories on skills.sh copy others' skills and show large install
// counts; these are listed first.
var skillVerifiedOwners = map[string]bool{
	"anthropics": true, "openai": true, "vercel": true, "vercel-labs": true, "duckdb": true, "motherduckdb": true,
	"huggingface": true, "google-gemini": true, "microsoft": true, "github": true, "cloudflare": true,
	"supabase": true, "stripe": true, "firecrawl": true, "tavily-ai": true, "hifinab": true,
}

func skillVerifiedOwner(source string) bool {
	owner, _, _ := strings.Cut(source, "/")
	return skillVerifiedOwners[strings.ToLower(owner)]
}

// skillSuggestion is a skill hi suggests when nothing is searched for.
type skillSuggestion struct {
	Source, Skill, Why string
}

// skillSuggestions are what the selector shows before a search: well-made
// skills from the tools' makers.
var skillSuggestions = []skillSuggestion{
	{"anthropics/skills", "pdf", "Read, fill, merge, and create PDFs"},
	{"anthropics/skills", "docx", "Create and edit Word documents"},
	{"anthropics/skills", "xlsx", "Create and edit Excel workbooks"},
	{"anthropics/skills", "pptx", "Create and edit PowerPoint decks"},
	{"anthropics/skills", "webapp-testing", "Test web apps with Playwright"},
	{"anthropics/skills", "skill-creator", "Write and improve skills"},
	{"anthropics/skills", "frontend-design", "Distinctive web interfaces"},
	{"vercel-labs/agent-browser", "agent-browser", "Browse and automate websites"},
	{"duckdb/duckdb-skills", "query", "SQL on files and databases with DuckDB"},
	{"duckdb/duckdb-skills", "read-file", "Read CSV, Parquet, JSON, and Excel files"},
	{"huggingface/skills", "huggingface-datasets", "Find and use Hugging Face datasets"},
	{"anthropics/knowledge-work-plugins", "data-visualization", "Charts and data visualisation"},
}

// ---------------------------------------------------------------------------
// what a skill holds

// skillMentionPatterns are tools a skill's text may expect to be there.
var skillMentionPatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"LibreOffice", regexp.MustCompile(`(?i)\b(libreoffice|soffice)\b`)},
	{"pandoc", regexp.MustCompile(`(?i)\bpandoc\b`)},
	{"Playwright", regexp.MustCompile(`(?i)\bplaywright\b`)},
	{"Chrome", regexp.MustCompile(`(?i)\b(chromium|chrome)\b`)},
	{"npx", regexp.MustCompile(`\bnpx\b`)},
	{"npm", regexp.MustCompile(`\bnpm (i|install)\b`)},
	{"pip", regexp.MustCompile(`\bpip3? install\b`)},
	{"uv", regexp.MustCompile(`\buv (pip|run|add|sync)\b`)},
	{"Docker", regexp.MustCompile(`(?i)\bdocker\b`)},
	{"ffmpeg", regexp.MustCompile(`(?i)\bffmpeg\b`)},
	{"openpyxl", regexp.MustCompile(`\bopenpyxl\b`)},
	{"pandas", regexp.MustCompile(`\bpandas\b`)},
	{"markitdown", regexp.MustCompile(`\bmarkitdown\b`)},
	{"pypdf", regexp.MustCompile(`\bpypdf\b`)},
	{"poppler", regexp.MustCompile(`\b(pdftotext|pdftoppm|poppler)\b`)},
	{"DuckDB", regexp.MustCompile(`(?i)\bduckdb\b`)},
}

var skillAPIKeyPattern = regexp.MustCompile(`\b[A-Z][A-Z0-9_]*_(API_KEY|TOKEN|SECRET)\b`)

// skillMentions lists the tools and keys a skill's markdown mentions.
func skillMentions(dir string) []string {
	var text strings.Builder
	filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() && strings.HasSuffix(strings.ToLower(path), ".md") {
			if data, err := os.ReadFile(path); err == nil && len(data) < 1<<20 {
				text.Write(data)
				text.WriteByte('\n')
			}
		}
		return nil
	})
	var mentions []string
	for _, mention := range skillMentionPatterns {
		if mention.pattern.MatchString(text.String()) {
			mentions = append(mentions, mention.name)
		}
	}
	seen := map[string]bool{}
	for _, key := range skillAPIKeyPattern.FindAllString(text.String(), -1) {
		if !seen[key] {
			seen[key] = true
			mentions = append(mentions, key)
		}
	}
	return mentions
}

// skillFiles describes a skill's files: how many, and its scripts by
// language.
func skillFiles(dir string) string {
	languages := map[string]string{".py": "Python", ".js": "JavaScript", ".mjs": "JavaScript", ".cjs": "JavaScript",
		".ts": "TypeScript", ".sh": "shell", ".bash": "shell", ".rb": "Ruby", ".go": "Go", ".ps1": "PowerShell"}
	files, scripts := 0, 0
	kinds := map[string]bool{}
	filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return nil
		}
		files++
		if language, ok := languages[strings.ToLower(filepath.Ext(path))]; ok {
			scripts++
			kinds[language] = true
		}
		return nil
	})
	summary := plural(files, "file")
	if scripts > 0 {
		var names []string
		for name := range kinds {
			names = append(names, name)
		}
		sort.Strings(names)
		summary += fmt.Sprintf(", %s (%s)", plural(scripts, "script"), strings.Join(names, ", "))
	}
	return summary
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// fetchSkillPage reads a skill's SKILL.md from GitHub at the usual paths,
// for the selector's details; nothing is installed.
func fetchSkillPage(source, skill string) (map[string]string, error) {
	if skillLookupsOff() {
		return nil, errSkillLookupsOff
	}
	for _, path := range []string{"skills/" + skill + "/SKILL.md", skill + "/SKILL.md", ".claude/skills/" + skill + "/SKILL.md", "SKILL.md"} {
		response, err := skillHTTP.Get(skillRawURL() + "/" + source + "/HEAD/" + path)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			continue
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		meta := parseSkillFrontmatter(data)
		if path == "SKILL.md" && meta["name"] != skill {
			continue
		}
		return meta, nil
	}
	return nil, fmt.Errorf("no SKILL.md for %s in %s at the usual paths", skill, source)
}
