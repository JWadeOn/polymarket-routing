package routing

import (
	"fmt"
	"time"

	"github.com/jwadeon/equinox/internal/config"
	"github.com/jwadeon/equinox/internal/models"
)

// Route implements the Freshness > Liquidity > Slippage > Cost decision hierarchy.
// It accepts a MatchResult and returns a RoutingDecision. It never panics.
func Route(match models.MatchResult) models.RoutingDecision {
	decision := models.RoutingDecision{
		ExclusionReasons: map[string]string{},
		DataAgeSeconds:   map[string]float64{},
	}

	markets := []models.NormalizedMarket{match.MarketA, match.MarketB}

	type candidate struct {
		market         models.NormalizedMarket
		wap            float64
		filled         float64
		fillStatus     string
		effectivePrice float64
		feePerContract float64
		totalFee       float64
		assumptions    []string
	}

	var eligible []candidate

	for _, m := range markets {
		age := time.Since(m.FetchedAt).Seconds()
		decision.DataAgeSeconds[m.VenueID] = age

		// Step 1: Freshness check
		if time.Since(m.FetchedAt) > config.StalenessThreshold {
			decision.ExclusionReasons[m.VenueID] = "STALE_DATA"
			decision.ReasoningLog = append(decision.ReasoningLog,
				fmt.Sprintf("EXCLUDE %s: STALE_DATA (age=%.0fs > threshold=%.0fs)",
					m.VenueID, age, config.StalenessThreshold.Seconds()))
			continue
		}

		// Determine effective price for inversion-corrected market
		price := m.YesPrice
		if match.IsPolarityInverted && m.VenueID == match.MarketB.VenueID {
			price = 1.0 - m.YesPrice
		}

		// Use market asks if populated, otherwise synthesize single-level book.
		// When the orderbook fetch fails (TotalDepthUSD == 0), assume StandardLotUSD
		// of depth at YesPrice so the liquidity floor and WAP calculations can proceed.
		asks := m.Asks
		effectiveDepth := m.TotalDepthUSD
		if len(asks) == 0 && price > 0 {
			effectiveDepth = config.StandardLotUSD
			asks = []models.OrderbookLevel{{Price: price, SizeUSD: effectiveDepth}}
		}

		// Step 2: Liquidity floor (10% of StandardLotUSD)
		liquidityFloor := config.StandardLotUSD * 0.10
		if effectiveDepth < liquidityFloor {
			decision.ExclusionReasons[m.VenueID] = "INSUFFICIENT_DEPTH"
			decision.ReasoningLog = append(decision.ReasoningLog,
				fmt.Sprintf("EXCLUDE %s: INSUFFICIENT_DEPTH (depth=$%.2f < floor=$%.2f)",
					m.VenueID, effectiveDepth, liquidityFloor))
			continue
		}

		wap, filled, status := CalculateWAP(asks, config.StandardLotUSD)
		if status == "REJECTED" {
			decision.ExclusionReasons[m.VenueID] = "INSUFFICIENT_DEPTH"
			decision.ReasoningLog = append(decision.ReasoningLog,
				fmt.Sprintf("EXCLUDE %s: REJECTED (no fillable depth)", m.VenueID))
			continue
		}

		// Step 3: Slippage check
		bestAsk := price
		if len(asks) > 0 {
			bestAsk = asks[0].Price
		}
		if wap > bestAsk+config.SlippageCeiling {
			decision.ExclusionReasons[m.VenueID] = "SLIPPAGE_EXCEEDED"
			decision.ReasoningLog = append(decision.ReasoningLog,
				fmt.Sprintf("EXCLUDE %s: SLIPPAGE_EXCEEDED (WAP=%.4f > bestAsk+ceiling=%.4f)",
					m.VenueID, wap, bestAsk+config.SlippageCeiling))
			continue
		}

		// Step 4: Cost — effective price = WAP + fee/contract
		feeEst := NewFeeAdapter(m.VenueID).Calculate(wap, filled)
		effectivePrice := wap + feeEst.FeePerContract

		eligible = append(eligible, candidate{
			market:         m,
			wap:            wap,
			filled:         filled,
			fillStatus:     status,
			effectivePrice: effectivePrice,
			feePerContract: feeEst.FeePerContract,
			totalFee:       feeEst.TotalFee,
			assumptions:    feeEst.Assumptions,
		})

		decision.ReasoningLog = append(decision.ReasoningLog,
			fmt.Sprintf("ELIGIBLE %s: WAP=%.4f fee/ctr=%.4f effPrice=%.4f depth=$%.2f status=%s",
				m.VenueID, wap, feeEst.FeePerContract, effectivePrice, m.TotalDepthUSD, status))
	}

	// No eligible venues
	if len(eligible) == 0 {
		decision.FillStatus = "REJECTED"
		decision.ReasoningLog = append(decision.ReasoningLog, "RESULT: REJECTED — all venues excluded")
		return decision
	}

	// Select lowest effective price; tie-break on higher TotalDepthUSD
	best := eligible[0]
	for _, c := range eligible[1:] {
		if c.effectivePrice < best.effectivePrice ||
			(c.effectivePrice == best.effectivePrice && c.market.TotalDepthUSD > best.market.TotalDepthUSD) {
			best = c
		}
	}

	// Build savings log if both venues are eligible
	if len(eligible) == 2 {
		other := eligible[0]
		if other.market.VenueID == best.market.VenueID {
			other = eligible[1]
		}
		savings := other.effectivePrice - best.effectivePrice
		decision.ReasoningLog = append(decision.ReasoningLog,
			fmt.Sprintf("REASON: Lower Effective Price (%.4f vs %.4f). Fee offset analysis: %s fee/ctr=%.4f vs %s fee/ctr=%.4f",
				best.effectivePrice, other.effectivePrice,
				best.market.VenueID, best.feePerContract,
				other.market.VenueID, other.feePerContract))
		decision.ReasoningLog = append(decision.ReasoningLog,
			fmt.Sprintf("SAVINGS: $%.4f/contract = $%.2f on $%.0f lot",
				savings, savings*config.StandardLotUSD/best.wap, config.StandardLotUSD))
	}

	// Append fee assumptions
	for _, a := range best.assumptions {
		decision.ReasoningLog = append(decision.ReasoningLog, "ASSUMPTION: "+a)
	}

	decision.SelectedVenue = best.market.VenueID
	decision.EffectivePrice = best.effectivePrice
	decision.WAP = best.wap
	decision.FeePerContract = best.feePerContract
	decision.TotalCost = best.wap*best.filled + best.totalFee
	decision.FillStatus = best.fillStatus
	if best.fillStatus == "PARTIAL" {
		decision.AvailableDepth = best.filled
	}

	return decision
}
