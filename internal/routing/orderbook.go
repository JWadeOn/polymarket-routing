package routing

import (
	"math"

	"github.com/jwadeon/equinox/internal/models"
)

// CalculateWAP walks asks ascending until lotUSD is filled.
// Returns wap (weighted average price), filled (USD actually filled), and status.
// status is "FULL", "PARTIAL", or "REJECTED".
func CalculateWAP(asks []models.OrderbookLevel, lotUSD float64) (wap float64, filled float64, status string) {
	remaining := lotUSD
	totalCost := 0.0

	for _, level := range asks { // asks must be sorted ascending
		if remaining <= 0 {
			break
		}
		take := math.Min(remaining, level.SizeUSD)
		totalCost += take * level.Price
		remaining -= take
	}

	filled = lotUSD - remaining
	if filled == 0 {
		return 0, 0, "REJECTED"
	}
	wap = totalCost / filled

	if remaining > 0 {
		return wap, filled, "PARTIAL"
	}
	return wap, filled, "FULL"
}
