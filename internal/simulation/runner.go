// Package simulation wires all layers and formats the reasoning log output.
package simulation

import (
	"fmt"
	"sort"

	"github.com/jwadeon/equinox/internal/config"
	"github.com/jwadeon/equinox/internal/connector"
	"github.com/jwadeon/equinox/internal/matching"
	"github.com/jwadeon/equinox/internal/models"
	"github.com/jwadeon/equinox/internal/routing"
)

const topN = 5

// Run fetches markets from both connectors, finds matched pairs,
// simulates a $500 YES order on the top-5 by confidence, and prints
// the reasoning log for each.
func Run(poly connector.VenueConnector, kalshi connector.VenueConnector) error {
	polyMarkets, err := poly.FetchMarkets("")
	if err != nil {
		return fmt.Errorf("polymarket fetch: %w", err)
	}
	fmt.Printf("Fetched %d Polymarket markets\n", len(polyMarkets))

	kalshiMarkets, err := kalshi.FetchMarkets("")
	if err != nil {
		return fmt.Errorf("kalshi fetch: %w", err)
	}
	fmt.Printf("Fetched %d Kalshi markets\n", len(kalshiMarkets))

	matches := matching.FindMatches(polyMarkets, kalshiMarkets)
	fmt.Printf("Found %d matched pairs (confidence ≥ %.2f)\n\n", len(matches), config.MatchConfidenceThreshold)

	if len(matches) == 0 {
		fmt.Println("No matched pairs found. Exiting.")
		return nil
	}

	// Sort by confidence descending
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Confidence > matches[j].Confidence
	})

	// Take top N
	top := matches
	if len(top) > topN {
		top = top[:topN]
	}

	for i, match := range top {
		// Fetch orderbooks for both markets
		if err := poly.FetchOrderbook(&match.MarketA); err != nil {
			fmt.Printf("Warning: could not fetch Polymarket orderbook for %s: %v\n", match.MarketA.InternalID, err)
		}
		if err := kalshi.FetchOrderbook(&match.MarketB); err != nil {
			fmt.Printf("Warning: could not fetch Kalshi orderbook for %s: %v\n", match.MarketB.InternalID, err)
		}

		decision := routing.Route(match)

		fmt.Printf("\n[%d/%d]\n", i+1, len(top))
		PrintReasoningLog(match, decision)
	}

	return nil
}

// PrintReasoningLog emits the box-drawing character format from the spec.
func PrintReasoningLog(match models.MatchResult, decision models.RoutingDecision) {
	const (
		heavy  = "═══════════════════════════════════════════════════════════════"
		light  = "───────────────────────────────────────────────────────────────"
	)

	fmt.Println(heavy)
	fmt.Println("  PROJECT EQUINOX — ROUTING DECISION")
	fmt.Println(heavy)

	fmt.Printf("  Target Market : %s\n", match.MarketA.Title)
	fmt.Printf("  Standard Lot  : $%.0f USD (YES side)\n", config.StandardLotUSD)
	fmt.Printf("  Matched Pair  : Polymarket id=%s ↔ Kalshi %s\n",
		match.MarketA.InternalID, match.MarketB.InternalID)
	fmt.Printf("  Confidence    : %.2f (Title: %.2f, Date: %.2f, Category: %.2f)\n",
		match.Confidence, match.TitleScore, match.DateScore, match.CategoryScore)
	if match.IsPolarityInverted {
		fmt.Println("  Polarity      : INVERTED (Router uses 1.0 − Market_B.YesPrice)")
	} else {
		fmt.Println("  Polarity      : Aligned (no inversion)")
	}

	fmt.Println(light)
	fmt.Printf("  %-12s %-8s %-9s %-10s %-9s %s\n",
		"VENUE", "WAP", "FEE/CTR", "EFF.PRICE", "DEPTH", "STATUS")

	// Print both venues
	markets := []models.NormalizedMarket{match.MarketA, match.MarketB}
	for _, m := range markets {
		excluded, wasExcluded := decision.ExclusionReasons[m.VenueID]
		if wasExcluded {
			fmt.Printf("  %-12s %-8s %-9s %-10s %-9s %s\n",
				m.VenueID, "—", "—", "—", fmt.Sprintf("$%.0f", m.TotalDepthUSD), "EXCLUDED: "+excluded)
		} else if m.VenueID == decision.SelectedVenue {
			fmt.Printf("  %-12s %-8.4f %-9.4f %-10.4f $%-8.0f %s\n",
				m.VenueID, decision.WAP, decision.FeePerContract, decision.EffectivePrice,
				m.TotalDepthUSD, decision.FillStatus)
		} else {
			// Eligible but not selected: re-calculate for display
			fa := routing.NewFeeAdapter(m.VenueID)
			wap, filled, status := routing.CalculateWAP(m.Asks, config.StandardLotUSD)
			if len(m.Asks) == 0 {
				wap = m.YesPrice
				filled = config.StandardLotUSD
				status = "FULL"
			}
			feeEst := fa.Calculate(wap, filled)
			effP := wap + feeEst.FeePerContract
			fmt.Printf("  %-12s %-8.4f %-9.4f %-10.4f $%-8.0f %s\n",
				m.VenueID, wap, feeEst.FeePerContract, effP, m.TotalDepthUSD, status)
		}
	}

	fmt.Println(light)
	if decision.FillStatus == "REJECTED" {
		fmt.Println("  ✗ NO ROUTE   : All venues excluded")
	} else {
		fmt.Printf("  ✓ ROUTE TO   : %s\n", decision.SelectedVenue)
	}

	// Print reasoning log entries
	for _, line := range decision.ReasoningLog {
		fmt.Printf("  %s\n", line)
	}

	// Data age summary
	if len(decision.DataAgeSeconds) > 0 {
		fmt.Printf("  DATA AGE     : ")
		first := true
		for venue, age := range decision.DataAgeSeconds {
			if !first {
				fmt.Printf(" | ")
			}
			fmt.Printf("%s %.0fs", venue, age)
			first = false
		}
		fmt.Println()
	}

	// Assumptions
	fmt.Printf("  ASSUMPTIONS  : Polymarket taker fee %.2f%% peak (2026 US retail)\n",
		config.PolymarketPeakTakerFee*100)
	fmt.Printf("               : Kalshi fee_multiplier=%.2f (Series schema default)\n",
		config.KalshiFeeMultiplier)
	fmt.Println("               : Order treated as single-execution (no slippage modeled beyond WAP)")
	fmt.Println(heavy)
}
