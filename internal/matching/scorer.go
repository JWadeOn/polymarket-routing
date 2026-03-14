package matching

import (
	"fmt"
	"math"
	"strings"

	"github.com/jwadeon/equinox/internal/config"
	"github.com/jwadeon/equinox/internal/models"
)

// ScorePair computes TitleScore, DateScore, CategoryScore, and Confidence
// for two normalized markets. It returns the scores and an audit trail.
func ScorePair(a, b models.NormalizedMarket) (titleScore, dateScore, categoryScore, confidence float64, reasoning []string) {
	titleScore = jaccardSimilarity(a.TitleNorm, b.TitleNorm)
	reasoning = append(reasoning, fmt.Sprintf(
		"TitleScore=%.3f (Jaccard on %q vs %q)", titleScore, a.TitleNorm, b.TitleNorm,
	))

	dateScore = computeDateScore(a, b)
	reasoning = append(reasoning, fmt.Sprintf(
		"DateScore=%.3f (resolution delta = %.1fh, window = %.0fh×2)",
		dateScore,
		math.Abs(a.ResolutionTime.Sub(b.ResolutionTime).Hours()),
		config.ResolutionWindowHours,
	))

	categoryScore = computeCategoryScore(a.Category, b.Category)
	reasoning = append(reasoning, fmt.Sprintf(
		"CategoryScore=%.3f (%q vs %q)", categoryScore, a.Category, b.Category,
	))

	confidence = titleScore*config.TitleWeight +
		dateScore*config.DateWeight +
		categoryScore*config.CategoryWeight
	reasoning = append(reasoning, fmt.Sprintf(
		"Confidence=%.3f (%.2f×title + %.2f×date + %.2f×cat)",
		confidence, config.TitleWeight, config.DateWeight, config.CategoryWeight,
	))

	return
}

// jaccardSimilarity computes |A∩B|/|A∪B| on the token sets of two normalized title strings.
func jaccardSimilarity(aNorm, bNorm string) float64 {
	aTokens := tokenSet(aNorm)
	bTokens := tokenSet(bNorm)

	if len(aTokens) == 0 && len(bTokens) == 0 {
		return 1.0
	}

	intersection := 0
	for t := range aTokens {
		if bTokens[t] {
			intersection++
		}
	}
	union := len(aTokens) + len(bTokens) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func tokenSet(norm string) map[string]bool {
	set := map[string]bool{}
	for _, t := range strings.Fields(norm) {
		set[t] = true
	}
	return set
}

// computeDateScore returns max(0, 1 - delta/(ResolutionWindowHours*2)).
func computeDateScore(a, b models.NormalizedMarket) float64 {
	if a.ResolutionTime.IsZero() || b.ResolutionTime.IsZero() {
		// If either is unknown, give neutral score
		return 0.5
	}
	deltaHours := math.Abs(a.ResolutionTime.Sub(b.ResolutionTime).Hours())
	score := 1.0 - deltaHours/(config.ResolutionWindowHours*2)
	if score < 0 {
		return 0
	}
	return score
}

// adjacentCategories maps category pairs that are considered adjacent (partial credit).
var adjacentCategories = map[string]map[string]bool{
	"crypto":    {"economics": true},
	"economics": {"crypto": true},
}

// computeCategoryScore returns 1.0 (exact), 0.5 (adjacent), or 0.0.
func computeCategoryScore(catA, catB string) float64 {
	if catA == "" || catB == "" {
		return 0.5 // unknown category: neutral
	}
	if catA == catB {
		return 1.0
	}
	if adjacentCategories[catA][catB] {
		return 0.5
	}
	return 0.0
}
