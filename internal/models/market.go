// Package models defines the canonical data model shared by all layers.
// No venue-specific logic lives here.
package models

import "time"

// OrderbookLevel represents a single price level in a venue orderbook.
type OrderbookLevel struct {
	Price   float64 // Normalized to [0.0, 1.0]
	SizeUSD float64 // Available notional in USD at this level
}

// NormalizedMarket is the canonical representation of a binary prediction market.
// Every VenueConnector must translate its raw API response into this struct before
// passing data to the matching or routing layers.
type NormalizedMarket struct {
	VenueID        string           // "KALSHI" | "POLYMARKET"
	InternalID     string           // Native platform identifier
	TokenID        string           // Polymarket CLOB YES-outcome token ID (clobTokenIds[0])
	Title          string           // Original title (display)
	TitleNorm      string           // Preprocessed: lowercase, aliases expanded, stop words removed
	YesPrice       float64          // Best ask for YES outcome [0.0, 1.0]
	NoPrice        float64          // Best ask for NO outcome [0.0, 1.0]
	Asks           []OrderbookLevel // Sorted ascending by price
	TotalDepthUSD  float64          // sum(level.SizeUSD for level in Asks)
	Category       string           // "crypto" | "economics" | "politics" | "sports" | ""
	ResolutionTime time.Time        // Standardized UTC
	FetchedAt      time.Time        // Snapshot timestamp for staleness checks
}

// MatchResult is the output of the matching engine for a single market pair.
type MatchResult struct {
	MarketA            NormalizedMarket
	MarketB            NormalizedMarket
	Confidence         float64  // Composite score [0.0, 1.0]
	TitleScore         float64  // Jaccard component
	DateScore          float64  // Temporal proximity component
	CategoryScore      float64  // Category alignment component
	IsPolarityInverted bool     // True: A.YES == B.NO
	Reasoning          []string // Audit trail: one entry per scoring step
}

// RoutingDecision is the output of the routing engine for a single matched pair.
type RoutingDecision struct {
	SelectedVenue    string
	EffectivePrice   float64
	WAP              float64
	FeePerContract   float64
	TotalCost        float64
	FillStatus       string             // "FULL" | "PARTIAL" | "REJECTED"
	AvailableDepth   float64            // Populated on PARTIAL fills
	ExclusionReasons map[string]string  // {"KALSHI": "STALE_DATA"}
	DataAgeSeconds   map[string]float64 // {"KALSHI": 74.0}
	ReasoningLog     []string           // Human-readable audit trail
}
