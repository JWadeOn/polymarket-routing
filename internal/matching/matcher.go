package matching

import (
	"strings"

	"github.com/jwadeon/equinox/internal/config"
	"github.com/jwadeon/equinox/internal/models"
)

// inversionTokens are negation signals — XOR presence between two titles implies polarity inversion.
var inversionTokens = []string{"not", "no", "fail", "cut", "lower", "below", "miss"}

// DetectInversion returns true when exactly one of the two market titles contains a negation token.
// XOR: A has negation, B does not — or vice versa.
func DetectInversion(a, b models.NormalizedMarket) bool {
	aHasNeg := containsAny(a.TitleNorm, inversionTokens)
	bHasNeg := containsAny(b.TitleNorm, inversionTokens)
	return aHasNeg != bHasNeg
}

func containsAny(s string, tokens []string) bool {
	fields := strings.Fields(s)
	for _, f := range fields {
		for _, t := range tokens {
			if f == t {
				return true
			}
		}
	}
	return false
}

// MatchPair evaluates whether two markets are equivalent and returns a MatchResult.
// Returns ok=false when confidence is below config.MatchConfidenceThreshold.
func MatchPair(a, b models.NormalizedMarket) (models.MatchResult, bool) {
	titleScore, dateScore, catScore, confidence, reasoning := ScorePair(a, b)

	result := models.MatchResult{
		MarketA:       a,
		MarketB:       b,
		Confidence:    confidence,
		TitleScore:    titleScore,
		DateScore:     dateScore,
		CategoryScore: catScore,
		Reasoning:     reasoning,
	}

	if confidence < config.MatchConfidenceThreshold {
		return result, false
	}

	result.IsPolarityInverted = DetectInversion(a, b)
	if result.IsPolarityInverted {
		result.Reasoning = append(result.Reasoning, "PolarityInverted=true (XOR negation token detected)")
	}

	return result, true
}

// FindMatches returns all matched pairs from a cross-product of two market slices.
// Each pair is tested once (polymarket × kalshi).
func FindMatches(as, bs []models.NormalizedMarket) []models.MatchResult {
	var results []models.MatchResult
	for _, a := range as {
		for _, b := range bs {
			if r, ok := MatchPair(a, b); ok {
				results = append(results, r)
			}
		}
	}
	return results
}
