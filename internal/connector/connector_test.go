package connector

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jwadeon/equinox/internal/models"
)

// TestPriceNormalization asserts that both connectors normalize price fields
// to the same float64 value within 0.001 tolerance, using fixture JSON.
func TestPriceNormalization(t *testing.T) {
	t.Run("KalshiNormalize", func(t *testing.T) {
		// Synthesize a minimal Kalshi market JSON with yes_ask_dollars="0.6500"
		raw := kalshiMarket{
			Ticker:        "TEST-65",
			Title:         "Test market",
			YesAskDollars: "0.6500",
			NoAskDollars:  "0.3500",
			CloseTime:     "2026-06-01T00:00:00Z",
			EventTicker:   "KXTEST",
		}
		k := NewKalshiConnector()
		m, err := k.normalizeMarket(raw, time.Now())
		if err != nil {
			t.Fatalf("normalizeMarket: %v", err)
		}
		if m.YesPrice < 0.649 || m.YesPrice > 0.651 {
			t.Errorf("KalshiConnector YesPrice=%.4f, want 0.6500 ±0.001", m.YesPrice)
		}
	})

	t.Run("PolymarketNormalize", func(t *testing.T) {
		// Synthesize a minimal Polymarket market JSON with outcomePrices=["0.65","0.35"]
		raw := gammaMarket{
			ConditionID:   "0xABC",
			Question:      "Test market",
			OutcomePrices: `["0.65","0.35"]`,
			EndDate:       "2026-06-01T00:00:00Z",
		}
		p := NewPolymarketConnector()
		m, err := p.normalizeMarket(raw, time.Now())
		if err != nil {
			t.Fatalf("normalizeMarket: %v", err)
		}
		if m.YesPrice < 0.649 || m.YesPrice > 0.651 {
			t.Errorf("PolymarketConnector YesPrice=%.4f, want 0.65 ±0.001", m.YesPrice)
		}
	})

	t.Run("BothProduceSameValue", func(t *testing.T) {
		kRaw := kalshiMarket{
			Ticker:        "TEST-65",
			Title:         "Rates rise",
			YesAskDollars: "0.6500",
			NoAskDollars:  "0.3500",
			CloseTime:     "2026-06-01T00:00:00Z",
			EventTicker:   "KXTEST",
		}
		pRaw := gammaMarket{
			ConditionID:   "0xABC",
			Question:      "Rates rise",
			OutcomePrices: `["0.65","0.35"]`,
			EndDate:       "2026-06-01T00:00:00Z",
		}

		k := NewKalshiConnector()
		p := NewPolymarketConnector()
		km, kerr := k.normalizeMarket(kRaw, time.Now())
		pm, perr := p.normalizeMarket(pRaw, time.Now())
		if kerr != nil || perr != nil {
			t.Fatalf("normalize errors: k=%v p=%v", kerr, perr)
		}
		diff := km.YesPrice - pm.YesPrice
		if diff < -0.001 || diff > 0.001 {
			t.Errorf("price mismatch: Kalshi=%.4f Polymarket=%.4f diff=%.4f", km.YesPrice, pm.YesPrice, diff)
		}
	})
}

// TestFixturePolymarketParseable verifies the saved fixture parses without panic.
func TestFixturePolymarketParseable(t *testing.T) {
	data, err := os.ReadFile("../../testdata/polymarket_markets.json")
	if err != nil {
		t.Skip("fixture not found:", err)
	}
	// Serve fixture via httptest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
	defer srv.Close()

	p := &PolymarketConnector{client: srv.Client()}
	// Override the base URL by parsing the fixture directly
	var raw []gammaMarket
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("fixture parse: %v", err)
	}
	now := time.Now()
	var markets []models.NormalizedMarket
	for _, r := range raw {
		m, err := p.normalizeMarket(r, now)
		if err != nil {
			continue
		}
		markets = append(markets, m)
	}
	if len(markets) == 0 {
		t.Fatal("no markets parsed from polymarket fixture")
	}
	t.Logf("parsed %d Polymarket markets from fixture", len(markets))
}

// TestFixtureKalshiParseable verifies the saved fixture parses without panic.
func TestFixtureKalshiParseable(t *testing.T) {
	data, err := os.ReadFile("../../testdata/kalshi_markets.json")
	if err != nil {
		t.Skip("fixture not found:", err)
	}
	var envelope kalshiMarketsResponse
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("fixture parse: %v", err)
	}
	k := NewKalshiConnector()
	now := time.Now()
	var markets []models.NormalizedMarket
	for _, r := range envelope.Markets {
		m, err := k.normalizeMarket(r, now)
		if err != nil {
			continue
		}
		markets = append(markets, m)
	}
	if len(markets) == 0 {
		t.Fatal("no markets parsed from kalshi fixture")
	}
	t.Logf("parsed %d Kalshi markets from fixture", len(markets))
}
