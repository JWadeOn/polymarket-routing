// Package config centralizes all system constants for Project Equinox.
// Every numeric constant referenced elsewhere must come from this file.
package config

import "time"

const (
	// StandardLotUSD is the notional order size for all routing simulations.
	StandardLotUSD float64 = 500.0

	// StalenessThreshold is the maximum age of a venue snapshot before it is excluded.
	StalenessThreshold = 60 * time.Second

	// ResolutionWindowHours is the maximum acceptable difference in resolution times
	// between two markets before the date score degrades to zero.
	ResolutionWindowHours float64 = 72.0

	// MatchConfidenceThreshold is the minimum composite score to treat two markets as equivalent.
	MatchConfidenceThreshold float64 = 0.65

	// SlippageCeiling is the maximum acceptable WAP overshoot above best ask (absolute probability).
	SlippageCeiling float64 = 0.05

	// PolymarketPeakTakerFee is the variable taker fee at maximum uncertainty (p=0.50).
	PolymarketPeakTakerFee float64 = 0.0156

	// PolymarketFeeFloor is the minimum taker fee applied at probability extremes.
	PolymarketFeeFloor float64 = 0.001

	// KalshiFeeMultiplier is the quadratic fee coefficient from the Kalshi Series schema default.
	KalshiFeeMultiplier float64 = 0.07

	// Matching weights — must sum to 1.0.
	TitleWeight    float64 = 0.60
	DateWeight     float64 = 0.25
	CategoryWeight float64 = 0.15
)
