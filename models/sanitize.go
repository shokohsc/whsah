package models

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	archiveExtRE   = regexp.MustCompile(`(?i)\.(zip|rar|7z|tar\.gz|iso|bin|cue|tgz)$`)
	bracketsRE     = regexp.MustCompile(`[\[(][^\])]*[\])]`)
	sceneTagRE     = regexp.MustCompile(`- ?[A-Za-z0-9]+$`)
	versionRE      = regexp.MustCompile(`(?i)\bv?\d+(\.\d+)*\b`)
	revisionRE     = regexp.MustCompile(`(?i)\bRev(ision)?\s*[A-Z0-9]+\b`)
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
	cleaned = sceneTagRE.ReplaceAllString(cleaned, "")
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
