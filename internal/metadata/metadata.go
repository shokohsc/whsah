// Package metadata retrieves game metadata from several remote sources and
// cross-references the results into a single record.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"selfhosted-gaming-vdi/internal/logging"
)

// PCRequirements holds the PC system requirements reported by a source.
type PCRequirements struct {
	Minimum     string `json:"minimum,omitempty"`
	Recommended string `json:"recommended,omitempty"`
}

// Review is a single review of a game.
type Review struct {
	Source string `json:"source,omitempty"`
	Author string `json:"author,omitempty"`
	Text   string `json:"text,omitempty"`
	Score  int    `json:"score,omitempty"`
	URL    string `json:"url,omitempty"`
}

// Source records where a piece of metadata came from.
type Source struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

// Metadata is the cross-referenced metadata stored in the game row.
type Metadata struct {
	Title             string         `json:"title,omitempty"`
	PEGIRating        string         `json:"pegi_rating,omitempty"`
	ESRBRating        string         `json:"esrb_rating,omitempty"`
	Rating            float64        `json:"rating,omitempty"`
	Genres            []string       `json:"genres,omitempty"`
	Tags              []string       `json:"tags,omitempty"`
	ReleaseDate       string         `json:"release_date,omitempty"`
	PCSpecs           PCRequirements `json:"pc_specs,omitempty"`
	Platforms         []string       `json:"platforms,omitempty"`
	MetacriticScore   int            `json:"metacritic_score,omitempty"`
	SensCritiqueScore float64        `json:"senscritique_score,omitempty"`
	Developer         string         `json:"developer,omitempty"`
	Publisher         string         `json:"publisher,omitempty"`
	LiveStreams       []string       `json:"live_streams,omitempty"`
	VideoReplays      []string       `json:"video_replays,omitempty"`
	Reviews           []Review       `json:"reviews,omitempty"`
	Sources           []Source       `json:"sources,omitempty"`
}

// Provider fetches game metadata from one remote source.
type Provider interface {
	Name() string
	Fetch(ctx context.Context, title string) (*Metadata, error)
}

// Aggregator queries every provider and merges their metadata. Providers are
// queried in order: the first non-empty value wins, while lists are combined.
type Aggregator struct {
	Providers []Provider
	Logger    *slog.Logger
}

// New builds an aggregator from the given providers.
func New(providers ...Provider) *Aggregator {
	return &Aggregator{Providers: providers, Logger: slog.Default()}
}

// FetchMetadata implements the worker's MetadataFetcher, returning JSON.
func (a *Aggregator) FetchMetadata(ctx context.Context, title string) (string, error) {
	meta, err := a.Fetch(ctx, title)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Fetch queries all providers and returns the merged metadata. It fails only
// when every provider fails.
func (a *Aggregator) Fetch(ctx context.Context, title string) (*Metadata, error) {
	var (
		merged  Metadata
		errs    []error
		success int
	)
	for _, p := range a.Providers {
		meta, err := p.Fetch(ctx, title)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
			if a.Logger != nil {
				a.Logger.Warn("metadata source failed", "source", p.Name(), "title", title, "err", logging.SafeError(err))
			}
			continue
		}
		if meta == nil {
			continue
		}
		success++
		merge(&merged, meta)
	}
	if success == 0 {
		if len(errs) == 0 {
			return nil, errors.New("no metadata source configured")
		}
		return nil, fmt.Errorf("no metadata source succeeded: %w", errors.Join(errs...))
	}
	if merged.PEGIRating == "" {
		merged.PEGIRating = PEGIFromESRB(merged.ESRBRating)
	}
	return &merged, nil
}

// merge fills the empty fields of dst from src and combines list fields.
func merge(dst, src *Metadata) {
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if dst.PEGIRating == "" {
		dst.PEGIRating = src.PEGIRating
	}
	if dst.ESRBRating == "" {
		dst.ESRBRating = src.ESRBRating
	}
	if dst.Rating == 0 {
		dst.Rating = src.Rating
	}
	if dst.ReleaseDate == "" {
		dst.ReleaseDate = src.ReleaseDate
	}
	if dst.PCSpecs.Minimum == "" {
		dst.PCSpecs.Minimum = src.PCSpecs.Minimum
	}
	if dst.PCSpecs.Recommended == "" {
		dst.PCSpecs.Recommended = src.PCSpecs.Recommended
	}
	if dst.MetacriticScore == 0 {
		dst.MetacriticScore = src.MetacriticScore
	}
	if dst.SensCritiqueScore == 0 {
		dst.SensCritiqueScore = src.SensCritiqueScore
	}
	if dst.Developer == "" {
		dst.Developer = src.Developer
	}
	if dst.Publisher == "" {
		dst.Publisher = src.Publisher
	}
	dst.Genres = appendUnique(dst.Genres, src.Genres...)
	dst.Tags = appendUnique(dst.Tags, src.Tags...)
	dst.Platforms = appendUnique(dst.Platforms, src.Platforms...)
	dst.LiveStreams = appendUnique(dst.LiveStreams, src.LiveStreams...)
	dst.VideoReplays = appendUnique(dst.VideoReplays, src.VideoReplays...)
	dst.Reviews = appendReviews(dst.Reviews, src.Reviews...)
	dst.Sources = appendSources(dst.Sources, src.Sources...)
}

func appendUnique(dst []string, values ...string) []string {
	seen := make(map[string]bool, len(dst))
	for _, v := range dst {
		seen[strings.ToLower(v)] = true
	}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[strings.ToLower(v)] {
			continue
		}
		seen[strings.ToLower(v)] = true
		dst = append(dst, v)
	}
	return dst
}

func appendReviews(dst []Review, values ...Review) []Review {
	seen := make(map[string]bool, len(dst))
	for _, v := range dst {
		seen[strings.ToLower(v.Source+"|"+v.Author+"|"+v.Text)] = true
	}
	for _, v := range values {
		key := strings.ToLower(v.Source + "|" + v.Author + "|" + v.Text)
		if seen[key] {
			continue
		}
		seen[key] = true
		dst = append(dst, v)
	}
	return dst
}

func appendSources(dst []Source, values ...Source) []Source {
	seen := make(map[string]bool, len(dst))
	for _, v := range dst {
		seen[strings.ToLower(v.Name+"|"+v.URL)] = true
	}
	for _, v := range values {
		key := strings.ToLower(v.Name + "|" + v.URL)
		if v.Name == "" || seen[key] {
			continue
		}
		seen[key] = true
		dst = append(dst, v)
	}
	return dst
}

// PEGIFromESRB maps an ESRB rating to its PEGI equivalent, so a source that
// only carries ESRB still yields an age rating.
func PEGIFromESRB(esrb string) string {
	switch strings.ToUpper(strings.TrimSpace(esrb)) {
	case "E", "EC", "EVERYONE", "EARLY CHILDHOOD":
		return "3"
	case "E10+", "E10", "EVERYONE 10+":
		return "7"
	case "T", "TEEN":
		return "12"
	case "M", "MATURE", "AO", "ADULTS ONLY":
		return "18"
	default:
		return ""
	}
}

// normalizePEGI extracts a valid PEGI value from a free-form rating.
func normalizePEGI(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	for _, pegi := range []string{"18", "16", "12", "7", "3"} {
		if strings.Contains(value, pegi) {
			return pegi
		}
	}
	return ""
}

// normalizedTitle makes game titles comparable across sources.
func normalizedTitle(title string) string {
	title = strings.ToLower(title)
	title = strings.NewReplacer("™", "", "®", "", "©", "", "&", "and").Replace(title)
	var b strings.Builder
	for _, r := range title {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	fields := strings.Fields(b.String())
	return strings.Join(fields, " ")
}

// titlesMatch reports whether two titles refer to the same game, allowing for
// edition suffixes and version numbers.
func titlesMatch(a, b string) bool {
	a, b = normalizedTitle(a), normalizedTitle(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	// A longer title may add a version or edition after the exact title
	// ("Hades" matches "Hades v1.0"), but not a sequel ("Hades II").
	if strings.HasPrefix(b, a+" ") {
		return versionSuffix(strings.TrimPrefix(b, a+" "))
	}
	if strings.HasPrefix(a, b+" ") {
		return versionSuffix(strings.TrimPrefix(a, b+" "))
	}
	return false
}

func versionSuffix(suffix string) bool {
	fields := strings.Fields(suffix)
	if len(fields) == 0 {
		return false
	}
	first := fields[0]
	if first == "v" || (strings.HasPrefix(first, "v") && len(first) > 1 && first[1] >= '0' && first[1] <= '9') {
		return true
	}
	switch first {
	case "goty", "definitive", "ultimate", "complete", "deluxe", "edition",
		"remastered", "enhanced", "plus", "bonus", "build", "update", "repack",
		"digital", "game", "of", "the", "year":
		return true
	}
	return false
}
