// debug: investigate available market landscape across both venues.
package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jwadeon/equinox/internal/connector"
	"github.com/jwadeon/equinox/internal/matching"
	"github.com/jwadeon/equinox/internal/models"
)

func main() {
	poly := connector.NewPolymarketConnector()
	kalshi := connector.NewKalshiConnector()

	polyMarkets, _ := poly.FetchMarkets("")
	kalshiMarkets, _ := kalshi.FetchMarkets("")

	fmt.Printf("Polymarket: %d  Kalshi: %d\n\n", len(polyMarkets), len(kalshiMarkets))

	// Score ALL cross-product pairs
	type scored struct {
		a, b       models.NormalizedMarket
		title, date, cat, conf float64
	}
	var allPairs []scored
	for _, a := range polyMarkets {
		for _, b := range kalshiMarkets {
			ti, d, c, conf, _ := matching.ScorePair(a, b)
			allPairs = append(allPairs, scored{a, b, ti, d, c, conf})
		}
	}
	sort.Slice(allPairs, func(i, j int) bool { return allPairs[i].conf > allPairs[j].conf })

	fmt.Println("=== Top 20 pairs (all venues) ===")
	for i, s := range allPairs {
		if i >= 20 {
			break
		}
		fmt.Printf("[%d] conf=%.4f title=%.3f date=%.3f cat=%.3f\n", i+1, s.conf, s.title, s.date, s.cat)
		fmt.Printf("    POLY  res=%s cat=%s %q\n", s.a.ResolutionTime.Format("2006-01-02"), s.a.Category, s.a.Title)
		fmt.Printf("    norm: %q\n", s.a.TitleNorm)
		fmt.Printf("    KALSHI res=%s cat=%s %q\n", s.b.ResolutionTime.Format("2006-01-02"), s.b.Category, s.b.Title)
		fmt.Printf("    norm: %q\n", s.b.TitleNorm)
		// Show intersection and unique tokens
		aSet := tokenSet(s.a.TitleNorm)
		bSet := tokenSet(s.b.TitleNorm)
		var shared, aOnly, bOnly []string
		for t := range aSet {
			if bSet[t] {
				shared = append(shared, t)
			} else {
				aOnly = append(aOnly, t)
			}
		}
		for t := range bSet {
			if !aSet[t] {
				bOnly = append(bOnly, t)
			}
		}
		sort.Strings(shared)
		sort.Strings(aOnly)
		sort.Strings(bOnly)
		fmt.Printf("    shared(%d)=%v\n", len(shared), shared)
		fmt.Printf("    polyOnly=%v  kalshiOnly=%v\n\n", aOnly, bOnly)
	}

	// Show which pairs are above threshold
	fmt.Println("\n=== Pairs above threshold 0.65 ===")
	count := 0
	for _, s := range allPairs {
		if s.conf < 0.65 {
			break
		}
		count++
		fmt.Printf("[%d] conf=%.4f  POLY: %q  KALSHI: %q\n", count, s.conf, s.a.Title, s.b.Title)
	}
	fmt.Printf("\nTotal above threshold: %d\n", count)
}

func tokenSet(norm string) map[string]bool {
	set := map[string]bool{}
	for _, t := range strings.Fields(norm) {
		set[t] = true
	}
	return set
}
