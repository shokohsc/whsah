package metadata

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// DefaultFitGirlSearchURL is the FitGirl search endpoint. The {query}
// placeholder is replaced with the URL-escaped game title.
const DefaultFitGirlSearchURL = "https://fitgirl-repacks.site/?s={query}"

// FitGirlProvider scrapes FitGirl repacks through a browserless instance.
type FitGirlProvider struct {
	SearchURL   string
	Browserless *Browserless
}

func (p *FitGirlProvider) Name() string { return "FitGirl" }

type fitgirlResult struct {
	Title string
	URL   string
	Tags  []string
}

func (p *FitGirlProvider) Fetch(ctx context.Context, title string) (*Metadata, error) {
	if p.Browserless == nil {
		return nil, fmt.Errorf("fitgirl: browserless not configured")
	}
	searchURL := p.SearchURL
	if searchURL == "" {
		searchURL = DefaultFitGirlSearchURL
	}
	if strings.Contains(searchURL, "{query}") {
		searchURL = strings.ReplaceAll(searchURL, "{query}", url.QueryEscape(title))
	} else {
		u, err := url.Parse(searchURL)
		if err != nil {
			return nil, fmt.Errorf("fitgirl: search url: %w", err)
		}
		q := u.Query()
		q.Set("s", title)
		u.RawQuery = q.Encode()
		searchURL = u.String()
	}

	searchHTML, err := p.Browserless.Content(ctx, searchURL)
	if err != nil {
		return nil, err
	}
	results, err := parseFitGirlSearch(searchHTML)
	if err != nil {
		return nil, err
	}
	var match *fitgirlResult
	for i := range results {
		if titlesMatch(title, results[i].Title) {
			match = &results[i]
			break
		}
	}
	if match == nil {
		return nil, fmt.Errorf("fitgirl: no match for %q", title)
	}

	articleHTML, err := p.Browserless.Content(ctx, match.URL)
	if err != nil {
		return nil, err
	}
	meta, err := parseFitGirlArticle(articleHTML)
	if err != nil {
		return nil, err
	}
	if meta.Title == "" {
		meta.Title = cleanRepackTitle(match.Title)
	}
	meta.Tags = appendUnique(meta.Tags, match.Tags...)
	meta.Sources = appendSources(meta.Sources, Source{Name: p.Name(), URL: match.URL})
	return meta, nil
}

func parseFitGirlSearch(data []byte) ([]fitgirlResult, error) {
	root, err := parseHTML(data)
	if err != nil {
		return nil, fmt.Errorf("fitgirl: parse search: %w", err)
	}
	var results []fitgirlResult
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "article" && strings.Contains(attr(n, "class"), "category-lossless-repack") {
			heading := find(n, func(node *html.Node) bool {
				return node.Type == html.ElementNode && node.Data == "h1" && hasClass(node, "entry-title")
			})
			if heading != nil {
				link := find(heading, func(node *html.Node) bool {
					return node.Type == html.ElementNode && node.Data == "a" && attr(node, "href") != ""
				})
				if link != nil {
					result := fitgirlResult{
						Title: strings.TrimSpace(strings.Join(lines(link), " ")),
						URL:   attr(link, "href"),
					}
					for _, class := range strings.Fields(attr(n, "class")) {
						if strings.HasPrefix(class, "tag-") {
							result.Tags = appendUnique(result.Tags, strings.ReplaceAll(strings.TrimPrefix(class, "tag-"), "-", " "))
						}
					}
					results = append(results, result)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return results, nil
}

func parseFitGirlArticle(data []byte) (*Metadata, error) {
	root, err := parseHTML(data)
	if err != nil {
		return nil, fmt.Errorf("fitgirl: parse article: %w", err)
	}
	meta := &Metadata{}
	if heading := find(root, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "h1" && hasClass(n, "entry-title")
	}); heading != nil {
		meta.Title = cleanRepackTitle(strings.Join(lines(heading), " "))
	}
	content := find(root, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "entry-content")
	})
	if content == nil {
		return meta, nil
	}
	text := lines(content)
	meta.Tags = collectFitGirlTags(text)
	meta.Developer = valueForLabel(text, "Company:", "Developer:")
	meta.Publisher = valueForLabel(text, "Publisher:")
	if specs := fitGirlSpecs(text); specs != (PCRequirements{}) {
		meta.PCSpecs = specs
	}
	all := strings.Join(text, "\n")
	if m := regexp.MustCompile(`(?i)PEGI\s*[:\-]?\s*(\d{1,2})`).FindStringSubmatch(all); len(m) == 2 {
		meta.PEGIRating = normalizePEGI(m[1])
	}
	if m := regexp.MustCompile(`(?i)ESRB\s*[:\-]?\s*([A-Za-z0-9+]+)`).FindStringSubmatch(all); len(m) == 2 {
		meta.ESRBRating = strings.ToUpper(m[1])
	}
	return meta, nil
}

func cleanRepackTitle(title string) string {
	for _, separator := range []string{" – ", " - ", " — "} {
		if i := strings.Index(title, separator); i > 0 {
			return strings.TrimSpace(title[:i])
		}
	}
	return strings.TrimSpace(title)
}

func collectFitGirlTags(text []string) []string {
	var tags []string
	collecting := false
	for _, line := range text {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "genres/tags:") || strings.HasPrefix(lower, "genres:") {
			collecting = true
			line = strings.TrimSpace(line[strings.Index(line, ":")+1:])
			if line == "" {
				continue
			}
		}
		if !collecting {
			continue
		}
		// A new "Label:" line marks the end of the genre list.
		if !strings.HasPrefix(lower, "genres") {
			if i := strings.Index(line, ":"); i >= 0 {
				break
			}
		}
		for _, label := range []string{" Company:", " Developer:", " Publisher:", " Languages:"} {
			if i := strings.Index(strings.ToLower(line), strings.ToLower(label)); i >= 0 {
				line = line[:i]
			}
		}
		for _, part := range strings.Split(line, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				tags = appendUnique(tags, part)
			}
		}
	}
	return tags
}

func valueForLabel(text []string, labels ...string) string {
	for i, line := range text {
		for _, label := range labels {
			if strings.EqualFold(strings.TrimSuffix(line, ":"), strings.TrimSuffix(label, ":")) && i+1 < len(text) {
				return text[i+1]
			}
			if strings.HasPrefix(strings.ToLower(line), strings.ToLower(label)) {
				if value := strings.TrimSpace(line[len(label):]); value != "" {
					return value
				}
				if i+1 < len(text) {
					return text[i+1]
				}
			}
		}
	}
	return ""
}

func fitGirlSpecs(text []string) PCRequirements {
	var min, rec []string
	collecting := false
	for _, line := range text {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "system requirements") {
			collecting = true
			continue
		}
		if !collecting {
			continue
		}
		if strings.Contains(lower, "repack features") || strings.Contains(lower, "download mirrors") {
			break
		}
		if !strings.Contains(line, ":") {
			continue
		}
		if strings.Contains(lower, "recommended") {
			rec = append(rec, line)
		} else {
			min = append(min, line)
		}
	}
	return PCRequirements{Minimum: strings.Join(min, "; "), Recommended: strings.Join(rec, "; ")}
}
