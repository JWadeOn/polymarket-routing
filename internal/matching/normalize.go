// Package matching implements the rule-based market equivalence engine.
package matching

import (
	"regexp"
	"sort"
	"strings"
)

// aliasMap expands common abbreviations before token comparison.
var aliasMap = map[string]string{
	"btc":  "bitcoin",
	"eth":  "ethereum",
	"fed":  "federal reserve",
	"cpi":  "inflation",
	"usd":  "dollar",
	"100k": "100000",
	"1m":   "1000000",
	"q1":   "first quarter",
	"q2":   "second quarter",
	"q3":   "third quarter",
	"q4":   "fourth quarter",
	// Plural normalization: singular form used as canonical token
	"rates": "rate",
	"cuts":  "cut",
	// Month abbreviation normalization (skip "may" — common modal verb)
	"jan": "january",
	"feb": "february",
	"mar": "march",
	"apr": "april",
	"jun": "june",
	"jul": "july",
	"aug": "august",
	"sep": "september",
	"oct": "october",
	"nov": "november",
	"dec": "december",
	// FOMC = Federal Open Market Committee meeting
	"fomc": "meeting",
	// "between X and Y" is equivalent to "more than X" (lower bound match)
	"between": "more",
}

// stopWords are removed after alias expansion.
var stopWords = map[string]bool{
	"will": true, "the": true, "a": true, "an": true,
	"by": true, "in": true, "to": true, "of": true,
	"be": true, "is": true, "on": true, "at": true,
}

// reDollar matches patterns like $100,000 → numeric string.
var reDollar = regexp.MustCompile(`\$[\d,]+`)

// rePunct removes non-alphanumeric, non-space characters.
var rePunct = regexp.MustCompile(`[^a-z0-9\s]`)

// NormalizeTitle preprocesses a market title for token-level comparison.
// Steps: lowercase → strip dollar patterns → expand aliases → remove punctuation →
// remove stop words → sort tokens → join.
func NormalizeTitle(title string) string {
	s := strings.ToLower(title)

	// Replace $X,XXX patterns with numeric string (strip $ and commas)
	s = reDollar.ReplaceAllStringFunc(s, func(m string) string {
		return strings.ReplaceAll(strings.TrimPrefix(m, "$"), ",", "")
	})

	// Strip possessives before alias expansion so "Fed's" → "fed" → "federal reserve"
	s = strings.ReplaceAll(s, "'s", "")
	s = strings.ReplaceAll(s, "\u2019s", "") // curly apostrophe variant

	// Expand aliases — replace whole words
	for abbr, expansion := range aliasMap {
		// Word-boundary replacement: match abbr surrounded by spaces or string boundaries
		s = replaceWholeWord(s, abbr, expansion)
	}

	// Remove punctuation
	s = rePunct.ReplaceAllString(s, " ")

	// Tokenize
	tokens := strings.Fields(s)

	// Remove stop words and deduplicate
	seen := map[string]bool{}
	var filtered []string
	for _, t := range tokens {
		if stopWords[t] || seen[t] {
			continue
		}
		seen[t] = true
		filtered = append(filtered, t)
	}

	sort.Strings(filtered)
	return strings.Join(filtered, " ")
}

// replaceWholeWord replaces exact whole-word occurrences of old with new in s.
func replaceWholeWord(s, old, newVal string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if w == old {
			// Replace with potentially multi-word expansion
			words[i] = newVal
		}
	}
	return strings.Join(words, " ")
}
