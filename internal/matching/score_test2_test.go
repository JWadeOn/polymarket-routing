package matching

import (
	"fmt"
	"testing"
	"github.com/jwadeon/equinox/internal/models"
	"time"
)

func TestScoreCandidatePairs(t *testing.T) {
	tp := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	pairs := [][2]string{
		{
			"Will the Fed's upper bound reach 4.25% or higher before 2027?",
			"Will the upper bound of the federal funds rate be above 4.25% in September 2026?",
		},
		{
			"Will the Fed's upper bound reach 5.0% or higher before 2027?",
			"Will the upper bound of the federal funds rate be above 5.25% in September 2026?",
		},
		{
			"Will the Fed's lower bound reach 2.0% or lower before 2027?",
			"Will the upper bound of the federal funds rate be above 4.25% in April 2027?",
		},
		{
			"Will inflation reach more than 4% in 2026?",
			"Will the rate of CPI inflation be above 3.7% for the year ending in June 2026?",
		},
		{
			"Will the Fed decrease interest rates by 25 bps after the April 2026 meeting?",
			"Will the upper bound of the federal funds rate be above 4.25% in April 2026?",
		},
		{
			"Fed emergency rate cut before 2027?",
			"Will the Federal Reserve cut rates before 2027?",
		},
	}
	for _, p := range pairs {
		a := models.NormalizedMarket{Title: p[0], TitleNorm: NormalizeTitle(p[0]), ResolutionTime: tp, Category: "economics", FetchedAt: tp}
		b := models.NormalizedMarket{Title: p[1], TitleNorm: NormalizeTitle(p[1]), ResolutionTime: tp, Category: "economics", FetchedAt: tp}
		ts, ds, cs, conf, _ := ScorePair(a, b)
		fmt.Printf("conf=%.3f (t=%.3f d=%.3f c=%.3f)\n  A: %q\n  normA: %s\n  B: %q\n  normB: %s\n\n",
			conf, ts, ds, cs, p[0], a.TitleNorm, p[1], b.TitleNorm)
	}
}
