package models

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	archiveExtRE   = regexp.MustCompile(`(?i)\.(zip|rar|7z|tar\.gz|iso|bin|cue|tgz|nsp|xci)$`)
	bracketsRE     = regexp.MustCompile(`[\[(][^\])]*[\])]`)
	repackRE       = regexp.MustCompile(`(?i)[._\s-]+(fitgirl|dodi|codex|cpy|skidrow|razor1911|elamigos|plaza|rune|gog|empress|christsnatcher|kaos|igg|onlinefix|corepack|darksiid|hoodlum|reload|reloaded|anadius|tinyiso)[._\s-]?.*$`)
	repackDotRE    = regexp.MustCompile(`(?i)\.[ReEpPaAcCkK]+-?[A-Za-z0-9._]+$`)
	sceneTagRE     = regexp.MustCompile(`(?i)[._\s-]+(codex|cpy|skidrow|razor1911|plaza|rune|fitgirl|dodi|elamigos|empress|hoodlum|reload|reloaded|corepack)[._\s-]*$`)
	sceneTagSimple = regexp.MustCompile(`- ?[A-Za-z0-9]{2,}$`)
	versionRE      = regexp.MustCompile(`(?i)\bv?\d+(\.\d+)*([._-]?(build|patch|update))?\b`)
	revisionRE     = regexp.MustCompile(`(?i)\bRev(ision)?\s*[A-Z0-9]+\b`)
	germanRE       = regexp.MustCompile(`(?i)[._\s-]+(german|french|italian|spanish|russian|multi\d*|multilang)[._\s-]*$`)
	properRE       = regexp.MustCompile(`(?i)[._\s-]+proper[._\s-]*$`)
	readnfoRE      = regexp.MustCompile(`(?i)[._\s-]+(readnfo|nfo|incl)[._\s-]*$`)
	separatorsRE   = regexp.MustCompile(`[._]+`)
	extraSpacesRE  = regexp.MustCompile(`\s{2,}`)
	romanNumeralRE = regexp.MustCompile(`(?i)^(i|ii|iii|iv|v|vi|vii|viii|ix|x|xi|xii|xiii|xiv|xv|xvi|xvii|xviii|xix|xx)$`)
)

// SanitizeGameName turns a folder or file name into a searchable game title:
// it strips archive extensions, bracketed tags, scene groups and version
// suffixes, then title-cases the result.
func SanitizeGameName(rawName string) string {
	cleaned := archiveExtRE.ReplaceAllString(rawName, "")
	cleaned = bracketsRE.ReplaceAllString(cleaned, "")
	// strip repack/scene suffixes repeatedly
	for i := 0; i < 3; i++ {
		prev := cleaned
		cleaned = repackDotRE.ReplaceAllString(cleaned, "")
		cleaned = repackRE.ReplaceAllString(cleaned, "")
		cleaned = sceneTagRE.ReplaceAllString(cleaned, "")
		cleaned = sceneTagSimple.ReplaceAllString(cleaned, "")
		cleaned = germanRE.ReplaceAllString(cleaned, "")
		cleaned = properRE.ReplaceAllString(cleaned, "")
		cleaned = readnfoRE.ReplaceAllString(cleaned, "")
		if cleaned == prev {
			break
		}
	}
	cleaned = versionRE.ReplaceAllString(cleaned, "")
	cleaned = revisionRE.ReplaceAllString(cleaned, "")
	cleaned = separatorsRE.ReplaceAllString(cleaned, " ")
	cleaned = extraSpacesRE.ReplaceAllString(cleaned, " ")
	cleaned = strings.TrimSpace(cleaned)
	return smartTitleCase(cleaned)
}

// smartTitleCase title-cases words while keeping acronyms and Roman numerals.
func smartTitleCase(s string) string {
	words := strings.Fields(s)
	for i, word := range words {
		if (len(word) > 1 && word == strings.ToUpper(word)) || romanNumeralRE.MatchString(word) {
			continue
		}
		runes := []rune(strings.ToLower(word))
		runes[0] = unicode.ToUpper(runes[0])
		words[i] = string(runes)
	}
	return strings.Join(words, " ")
}
