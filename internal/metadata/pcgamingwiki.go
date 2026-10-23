package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// PCGamingWikiProvider queries the PCGamingWiki MediaWiki API and parses the
// game's infobox and system requirements.
type PCGamingWikiProvider struct {
	BaseURL string
	Client  *http.Client
}

func (p *PCGamingWikiProvider) Name() string { return "PCGamingWiki" }

func (p *PCGamingWikiProvider) Fetch(ctx context.Context, title string) (*Metadata, error) {
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://www.pcgamingwiki.com"
	}
	page, pageURL, err := p.findPage(ctx, base, title)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Parse struct {
			Wikitext struct {
				Text string `json:"*"`
			} `json:"wikitext"`
		} `json:"parse"`
	}
	q := url.Values{"action": {"parse"}, "page": {page}, "prop": {"wikitext"}, "format": {"json"}}
	if err := p.getJSON(ctx, base+"/w/api.php?"+q.Encode(), &parsed); err != nil {
		return nil, err
	}
	text := parsed.Parse.Wikitext.Text
	if text == "" {
		return nil, fmt.Errorf("pcgamingwiki: empty page %q", page)
	}

	meta := &Metadata{
		Title:           page,
		PEGIRating:      normalizePEGI(wikitextParam(text, "pegi")),
		ESRBRating:      strings.ToUpper(strings.TrimSpace(wikitextParam(text, "esrb"))),
		ReleaseDate:     pcgwReleaseDate(text),
		MetacriticScore: pcgwMetacritic(text),
		PCSpecs:         pcgwRequirements(text),
		Genres:          splitTaxonomy(text, "genres"),
		Tags:            splitTaxonomy(text, "themes"),
		Sources:         []Source{{Name: p.Name(), URL: pageURL}},
	}
	meta.Developer = firstTemplateValue(text, `\{\{Infobox game/row/developer\|([^}|]+)`)
	meta.Publisher = firstTemplateValue(text, `\{\{Infobox game/row/publisher\|([^}|]+)`)
	for _, platform := range pcgwPlatforms(text) {
		meta.Platforms = appendUnique(meta.Platforms, platform)
	}
	return meta, nil
}

// findPage returns the wiki page title and URL for the game. It prefers an
// exact title match and falls back to the search API.
func (p *PCGamingWikiProvider) findPage(ctx context.Context, base, title string) (string, string, error) {
	var exact struct {
		Query struct {
			Pages map[string]struct {
				Title   string `json:"title"`
				Missing string `json:"missing"`
			} `json:"pages"`
		} `json:"query"`
	}
	q := url.Values{"action": {"query"}, "titles": {title}, "format": {"json"}, "redirects": {"1"}}
	if err := p.getJSON(ctx, base+"/w/api.php?"+q.Encode(), &exact); err == nil {
		for _, page := range exact.Query.Pages {
			if page.Missing == "" && strings.EqualFold(normalizedTitle(page.Title), normalizedTitle(title)) {
				return page.Title, pcgwPageURL(base, page.Title), nil
			}
		}
	}

	var search struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	q = url.Values{"action": {"query"}, "list": {"search"}, "srsearch": {title}, "srlimit": {"10"}, "format": {"json"}}
	if err := p.getJSON(ctx, base+"/w/api.php?"+q.Encode(), &search); err != nil {
		return "", "", err
	}
	for _, result := range search.Query.Search {
		if titlesMatch(title, result.Title) {
			return result.Title, pcgwPageURL(base, result.Title), nil
		}
	}
	return "", "", fmt.Errorf("pcgamingwiki: no match for %q", title)
}

func pcgwPageURL(base, page string) string {
	return base + "/wiki/" + url.PathEscape(strings.ReplaceAll(page, " ", "_"))
}

func wikitextParam(text, name string) string {
	re := regexp.MustCompile(`(?m)^\|[ \t]*` + regexp.QuoteMeta(name) + `[ \t]*=[ \t]*(.*)$`)
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func firstTemplateValue(text, pattern string) string {
	m := regexp.MustCompile(pattern).FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func splitTaxonomy(text, name string) []string {
	re := regexp.MustCompile(`\{\{Infobox game/row/taxonomy/` + regexp.QuoteMeta(name) + `\s*\|([^}]*)\}\}`)
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return nil
	}
	var out []string
	for _, part := range strings.Split(m[1], ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = appendUnique(out, part)
		}
	}
	return out
}

func pcgwReleaseDate(text string) string {
	re := regexp.MustCompile(`\{\{Infobox game/row/date\|([^}|]+)\|([^}|]+)`)
	var first string
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		platform, date := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
		if first == "" {
			first = date
		}
		if strings.EqualFold(platform, "Windows") || strings.EqualFold(platform, "PC") {
			return date
		}
	}
	return first
}

func pcgwMetacritic(text string) int {
	m := regexp.MustCompile(`\{\{Infobox game/row/reception\|Metacritic\|[^|]*\|(\d+)`).FindStringSubmatch(text)
	if len(m) < 2 {
		return 0
	}
	var score int
	fmt.Sscanf(m[1], "%d", &score)
	return score
}

func pcgwPlatforms(text string) []string {
	re := regexp.MustCompile(`\{\{Infobox game/row/date\|([^}|]+)`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		platform := strings.TrimSpace(m[1])
		switch strings.ToLower(platform) {
		case "windows", "pc":
			platform = "PC"
		case "os x", "macos":
			platform = "macOS"
		case "linux":
			platform = "Linux"
		}
		out = appendUnique(out, platform)
	}
	return out
}

// pcgwRequirements extracts the Windows system requirements block.
func pcgwRequirements(text string) PCRequirements {
	blocks := regexp.MustCompile(`(?s)\{\{System requirements(.*?)\n\}\}`).FindAllStringSubmatch(text, -1)
	for _, block := range blocks {
		if len(block) < 2 || !strings.Contains(strings.ToLower(block[1]), "osfamily = windows") {
			continue
		}
		return PCRequirements{
			Minimum:     requirementLine(block[1], "min"),
			Recommended: requirementLine(block[1], "rec"),
		}
	}
	return PCRequirements{}
}

func requirementLine(block, prefix string) string {
	names := []struct{ key, label string }{
		{"OS", "OS"},
		{"CPU", "CPU"},
		{"RAM", "RAM"},
		{"GPU", "GPU"},
		{"VRAM", "VRAM"},
		{"HD", "Storage"},
	}
	var parts []string
	for _, name := range names {
		re := regexp.MustCompile(`(?m)^\|[ \t]*` + prefix + name.key + `[ \t]*=[ \t]*(.*)$`)
		m := re.FindStringSubmatch(block)
		if len(m) < 2 {
			continue
		}
		value := strings.TrimSpace(m[1])
		if value != "" {
			parts = append(parts, name.label+": "+value)
		}
	}
	return strings.Join(parts, "; ")
}

func (p *PCGamingWikiProvider) getJSON(ctx context.Context, endpoint string, out any) error {
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "whsah-metadata/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("pcgamingwiki: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("pcgamingwiki: decode: %w", err)
	}
	return nil
}
