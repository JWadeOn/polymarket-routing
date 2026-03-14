// Package routing implements the orderbook walk, fee calculation, and routing decision logic.
package routing

import "github.com/jwadeon/equinox/internal/config"

// FeeEstimate is the output of a FeeAdapter.Calculate call.
type FeeEstimate struct {
	TotalFee       float64
	FeeRate        float64
	FeePerContract float64
	ModelName      string
	Assumptions    []string // Flows into RoutingDecision.ReasoningLog
}

// FeeAdapter is the interface all venue fee models must satisfy.
type FeeAdapter interface {
	Calculate(price float64, tradeValueUSD float64) FeeEstimate
}

// KalshiFeeAdapter implements the quadratic fee model.
// Fee = KalshiFeeMultiplier × contracts × price × (1 − price)
type KalshiFeeAdapter struct{}

func (k KalshiFeeAdapter) Calculate(price, tradeValue float64) FeeEstimate {
	if price <= 0 {
		price = 0.0001
	}
	// Fee rate is quadratic: 0.07 × p × (1-p), peaking at p=0.50
	feeRate := config.KalshiFeeMultiplier * price * (1 - price)
	fee := tradeValue * feeRate
	contracts := tradeValue / price
	feePerContract := fee / contracts
	return FeeEstimate{
		TotalFee:       fee,
		FeeRate:        feeRate,
		FeePerContract: feePerContract,
		ModelName:      "kalshi_quadratic",
		Assumptions:    []string{"fee_multiplier=0.07 sourced from Series schema default"},
	}
}

// feeRegistry maps venueID → FeeAdapter. Owned entirely by this file.
var feeRegistry = map[string]FeeAdapter{
	"KALSHI":     KalshiFeeAdapter{},
	"POLYMARKET": PolymarketFeeAdapter{},
}

// NewFeeAdapter returns the FeeAdapter for the given venue ID.
// Falls back to KalshiFeeAdapter for unknown venues (logs no panic).
func NewFeeAdapter(venueID string) FeeAdapter {
	if fa, ok := feeRegistry[venueID]; ok {
		return fa
	}
	return KalshiFeeAdapter{}
}

// PolymarketFeeAdapter implements the variable taker fee with floor.
// BaseRate = max(PolymarketPeakTakerFee × 4 × price × (1−price), PolymarketFeeFloor)
// TotalFee = tradeValue × BaseRate
type PolymarketFeeAdapter struct{}

func (p PolymarketFeeAdapter) Calculate(price, tradeValue float64) FeeEstimate {
	baseRate := config.PolymarketPeakTakerFee * 4 * price * (1 - price)
	if baseRate < config.PolymarketFeeFloor {
		baseRate = config.PolymarketFeeFloor
	}
	fee := tradeValue * baseRate
	if price <= 0 {
		price = 0.0001
	}
	contracts := tradeValue / price
	return FeeEstimate{
		TotalFee:       fee,
		FeeRate:        baseRate,
		FeePerContract: fee / contracts,
		ModelName:      "polymarket_variable_taker",
		Assumptions: []string{
			"peak_taker_fee=1.56% at p=0.50 (2026 US retail schedule)",
			"fee_floor=0.1% applied at probability extremes",
		},
	}
}
