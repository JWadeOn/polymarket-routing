PROJECT EQUINOX
Final Technical Specification & Architecture Decision Record
Cross-Venue Prediction Market Aggregation & Routing Simulation
Language: Go 1.21+   ·   March 2026   ·   Spec-Driven Development

Locked Decisions — At a Glance
CATEGORY
LOCKED DECISION
Language
Go 1.21+. Removed Python. Infrastructure-first project; Go's type system and interface pattern are the right fit.
Matching Strategy
Rule-based: Normalized Jaccard token overlap + alias dictionary + date/category scoring. Fully explainable, no external model dependencies.
NLP / Embeddings
Out of scope for V1. Documented as V2 upgrade path. PRD does not require it; explainability > accuracy for this evaluation.
North Star Metric
Effective Price for a $500 Standard Lot (WAP + venue fees).
Fee Models
Kalshi: quadratic (0.07 × p × (1−p)). Polymarket: variable taker with 0.1% floor (1.56% peak at p=0.50).
Data Freshness
60-second TTL. Stale venues are excluded (not exceptions). Age logged in seconds per venue.
Resolution Window
72 hours. Accommodates oracle delays and timezone differences between venues.
Slippage Ceiling
5% absolute probability. If best ask = 0.65, max acceptable WAP = 0.70.
Partial Fills
Allowed. Reported as PARTIAL with available_depth_usd in routing log.
Polarity Inversion
Detected in Matcher. Corrected in Router via 1.0 − Market_B.YesPrice.
Persistence
None. Ephemeral in-memory. Optional JSON dump of matches and routing decisions.
Project Layout
Standard Go: /cmd, /internal/{config,models,connector,matching,routing,simulation}

Part 1: Pre-Search Checklist (Decision Log)
Phase 1 — Constraints & Scoping
QUESTION
DECISION
Scale & Load
Prototype-only. Single-user/evaluator execution. No concurrency requirements.
Budget
$0. All API usage is public unauthenticated REST. Optional Claude API for future AI matching: ~$1–5 estimated.
Compliance
None. No PII, no real-money transactions, no regulatory exposure.
North Star Metric
Effective Price for a $500 Standard Lot. Balances price, fees, and liquidity into one defensible number.
Market Types
Binary (Yes/No) only. Categorical and scalar markets are explicitly out of scope for V1.

Phase 2 — Architecture Discovery
QUESTION
DECISION
Language
Go 1.21+. Infrastructure-first project. Clean interface pattern for adapters. Compile-time contract enforcement. No NLP ecosystem needed for rule-based matching.
Hosting
Local CLI binary. go build produces a single executable. Zero deployment overhead.
Database
In-memory (structs/maps). No persistence required. Optional JSON output files for audit trail.
API Pattern
Modular monolith with interface-based adapters. VenueConnector and FeeAdapter interfaces decouple venue logic from core routing.
Third-Party APIs
Polymarket Gamma API (gamma-api.polymarket.com) + CLOB API (clob.polymarket.com). Kalshi REST API v2 (api.elections.kalshi.com/trade-api/v2). Both fully public for read-only access.
Testing Target
80%+ coverage on /internal/matching and /internal/routing packages. Fixture-based; no live API calls in tests.
Error Handling
Graceful degradation throughout. Stale/unavailable venue data results in exclusion with logged reason. System never panics on bad API data.

Phase 3 — Go vs Python: Final Rationale
The language decision was revisited multiple times during spec development. The final determination:

LAYER
Go Suitability
Notes
Config / constants
Excellent
time.Duration typing strictly better than raw int
Models / canonical schema
Excellent
Compile-time contract enforcement
Connectors / adapters
Excellent
Interface pattern is idiomatic Go
Fee adapters
Excellent
Pure math, zero dependencies
Orderbook walk / WAP
Excellent
Simple loop, fully deterministic
Routing decision logic
Excellent
Switch/case maps cleanly to hierarchy
Matching — rule-based
Good
Jaccard + alias dict covers real cases
Matching — NLP/embeddings
Poor → V2
Ecosystem gap. Documented upgrade path
CLI output / reasoning log
Excellent
fmt + structured logging

⚠  Eight of nine layers are Excellent in Go. The one weak layer (semantic NLP) is documented as a V2 upgrade path. The PRD does not require embedding-based matching — it requires documented, explainable methodology.

Part 2: Technical Specification
2.1  Project Layout
equinox/
├── cmd/
│   └── equinox/
│       └── main.go              # CLI entry point
├── internal/
│   ├── config/
│   │   └── config.go            # All constants (single source of truth)
│   ├── models/
│   │   └── market.go            # NormalizedMarket, MatchResult, RoutingDecision
│   ├── connector/
│   │   ├── connector.go         # VenueConnector interface
│   │   ├── polymarket.go        # Polymarket Gamma + CLOB adapter
│   │   └── kalshi.go            # Kalshi REST v2 adapter
│   ├── matching/
│   │   ├── normalize.go         # Title preprocessing + alias dictionary
│   │   ├── scorer.go            # Jaccard, date, category scoring
│   │   └── matcher.go           # ScorePair() orchestration + inversion detection
│   ├── routing/
│   │   ├── fees.go              # FeeAdapter interface + Kalshi/Polymarket impls
│   │   ├── orderbook.go         # CalculateWAP() + slippage check
│   │   └── router.go            # Route() — Freshness > Liquidity > Cost hierarchy
│   └── simulation/
│       └── runner.go            # Wires all layers; formats reasoning log
├── testdata/
│   ├── polymarket_markets.json  # Fixture: real API response snapshot
│   └── kalshi_markets.json      # Fixture: real API response snapshot
├── go.mod
├── go.sum
├── README.md
└── docs/
    ├── architecture.md
    ├── equivalence_logic.md
    └── routing_logic.md

2.2  Configuration & Constants
All system policies are centralized in a single file. Every constant in the locked spec table maps directly to a named Go constant here.

// internal/config/config.go
package config

import "time"

const (
    // Order sizing
    StandardLotUSD float64 = 500.0

    // Data freshness
    StalenessThreshold = 60 * time.Second

    // Matching thresholds
    ResolutionWindowHours float64 = 72.0
    MatchConfidenceThreshold float64 = 0.65

    // Routing constraints
    SlippageCeiling float64 = 0.05  // 5% absolute probability buffer

    // Fee model parameters
    PolymarketPeakTakerFee float64 = 0.0156  // 1.56% at p=0.50
    PolymarketFeeFloor     float64 = 0.001   // 0.1% minimum
    KalshiFeeMultiplier    float64 = 0.07

    // Matching weights (must sum to 1.0)
    TitleWeight    float64 = 0.60
    DateWeight     float64 = 0.25
    CategoryWeight float64 = 0.15
)

⚠  Matching weights are named constants, not magic numbers. If an evaluator questions why TitleWeight=0.60, the answer is documented in docs/equivalence_logic.md.

2.3  Canonical Data Model
Every venue's raw API response is translated into NormalizedMarket before reaching the Matcher or Router. No venue-specific logic exists downstream of the connector layer.

// internal/models/market.go
package models

import "time"

type OrderbookLevel struct {
    Price   float64 // Normalized to [0.0, 1.0]
    SizeUSD float64 // Available notional in USD at this level
}

type NormalizedMarket struct {
    VenueID        string          // "KALSHI" | "POLYMARKET"
    InternalID     string          // Native platform identifier
    Title          string          // Original title (display)
    TitleNorm      string          // Preprocessed: lowercase, aliases expanded, stop words removed
    YesPrice       float64         // Best ask for YES outcome [0.0, 1.0]
    NoPrice        float64         // Best ask for NO outcome [0.0, 1.0]
    Asks           []OrderbookLevel // Sorted ascending by price
    TotalDepthUSD  float64         // sum(level.SizeUSD for level in Asks)
    Category       string          // "crypto" | "economics" | "politics" | "sports" | ""
    ResolutionTime time.Time       // Standardized UTC
    FetchedAt      time.Time       // Snapshot timestamp for staleness checks
}

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

type RoutingDecision struct {
    SelectedVenue    string
    EffectivePrice   float64
    WAP              float64
    FeePerContract   float64
    TotalCost        float64
    FillStatus       string            // "FULL" | "PARTIAL" | "REJECTED"
    AvailableDepth   float64           // Populated on PARTIAL fills
    ExclusionReasons map[string]string // {"KALSHI": "STALE_DATA"}
    DataAgeSeconds   map[string]float64 // {"KALSHI": 74.0}
    ReasoningLog     []string          // Human-readable audit trail
}

2.4  Venue Connector Interface
// internal/connector/connector.go
package connector

import "github.com/your-org/equinox/internal/models"

type VenueConnector interface {
    // FetchMarkets returns normalized markets for the given category filter.
    // Returns partial results + error if some markets fail to parse.
    FetchMarkets(category string) ([]models.NormalizedMarket, error)

    // FetchOrderbook populates the Asks field for a specific market.
    FetchOrderbook(market *models.NormalizedMarket) error

    // VenueID returns the canonical venue identifier.
    VenueID() string
}

Key normalization responsibilities per connector:

CONNECTOR
NORMALIZATION RESPONSIBILITIES
PolymarketConnector
Parse outcomePrices JSON string → float64. Map condition_id → InternalID. Extract slug → Title. Convert CLOB ask levels [{price, size}] → []OrderbookLevel. Compute TotalDepthUSD. Parse endDate → ResolutionTime UTC.
KalshiConnector
Convert yes_ask_dollars string → float64 (already decimal, no division needed). Map ticker → InternalID. Map title + subtitle → Title. Convert orderbook [[cents, qty]] → []OrderbookLevel (price / 100.0, size = qty × price). Compute TotalDepthUSD. Parse close_time → ResolutionTime UTC.

2.5  Fee Adapters
// internal/routing/fees.go
package routing

import "github.com/your-org/equinox/internal/config"

type FeeEstimate struct {
    TotalFee       float64
    FeeRate        float64
    FeePerContract float64
    ModelName      string
    Assumptions    []string // Flows into RoutingDecision.ReasoningLog
}

type FeeAdapter interface {
    Calculate(price float64, tradeValueUSD float64) FeeEstimate
}

// KalshiFeeAdapter implements the quadratic fee model.
// Fee = 0.07 × contracts × price × (1 − price)
type KalshiFeeAdapter struct{}

func (k KalshiFeeAdapter) Calculate(price, tradeValue float64) FeeEstimate {
    contracts := tradeValue / price // approx contracts for tradeValue USD
    fee := config.KalshiFeeMultiplier * contracts * price * (1 - price)
    return FeeEstimate{
        TotalFee:       fee,
        FeeRate:        fee / tradeValue,
        FeePerContract: fee / contracts,
        ModelName:      "kalshi_quadratic",
        Assumptions:    []string{"fee_multiplier=0.07 sourced from Series schema default"},
    }
}

// PolymarketFeeAdapter implements the variable taker fee with floor.
// BaseRate = max(0.0156 × 4 × price × (1−price), 0.001)
// TotalFee = tradeValue × BaseRate
type PolymarketFeeAdapter struct{}

func (p PolymarketFeeAdapter) Calculate(price, tradeValue float64) FeeEstimate {
    baseRate := 0.0156 * 4 * price * (1 - price)
    if baseRate < config.PolymarketFeeFloor {
        baseRate = config.PolymarketFeeFloor
    }
    fee := tradeValue * baseRate
    contracts := tradeValue / price
    return FeeEstimate{
        TotalFee:       fee,
        FeeRate:        baseRate,
        FeePerContract: fee / contracts,
        ModelName:      "polymarket_variable_taker",
        Assumptions:    []string{
            "peak_taker_fee=1.56% at p=0.50 (2026 US retail schedule)",
            "fee_floor=0.1% applied at probability extremes",
        },
    }
}

2.6  Matching Engine
Title Preprocessing (normalize.go)
Applied to both markets before any similarity scoring. Produces TitleNorm.

// internal/matching/normalize.go

var aliasMap = map[string]string{
    "btc":              "bitcoin",
    "eth":              "ethereum",
    "fed":              "federal reserve",
    "cpi":              "inflation",
    "usd":              "dollar",
    "100k":             "100000",
    "1m":               "1000000",
    "q1": "first quarter", "q2": "second quarter",
    "q3": "third quarter", "q4": "fourth quarter",
}

var stopWords = map[string]bool{
    "will": true, "the": true, "a": true, "an": true,
    "by": true, "in": true, "to": true, "of": true,
    "be": true, "is": true, "on": true, "at": true,
}

func NormalizeTitle(title string) string {
    // 1. Lowercase
    // 2. Replace $X,XXX patterns → numeric string
    // 3. Expand aliases
    // 4. Remove punctuation
    // 5. Remove stop words
    // 6. Sort tokens (order-independent matching)
    // Returns: space-joined sorted token slice
}

Scoring Components (scorer.go)

COMPONENT
FORMULA & RATIONALE
Title Score (weight: 0.60)
Jaccard similarity on token sets: |A∩B| / |A∪B|. Applied to TitleNorm (post-preprocessing). Range [0.0, 1.0]. Highest weight because title is the primary semantic signal.
Date Score (weight: 0.25)
delta = |A.ResolutionTime − B.ResolutionTime| in hours. Score = max(0, 1 − delta/(72×2)). Full score at 0h delta, zero at 144h. Second-highest weight because date mismatches indicate different event instances.
Category Score (weight: 0.15)
1.0 if both categories match exactly. 0.5 if adjacent (e.g., economics/crypto both financial). 0.0 if no overlap. Lowest weight — a useful signal but not determinative alone.
Composite Score
Confidence = (TitleScore × 0.60) + (DateScore × 0.25) + (CategoryScore × 0.15). Threshold for MATCHED: ≥ 0.65 (config.MatchConfidenceThreshold).

Polarity Inversion Detection
Applied after composite scoring. If Confidence ≥ threshold, check for inversion:

// Inversion heuristic: look for negation tokens in one title but not the other
var inversionTokens = []string{"not", "no", "fail", "cut", "lower", "below", "miss"}

func DetectInversion(a, b NormalizedMarket) bool {
    aHasNeg := containsAny(a.TitleNorm, inversionTokens)
    bHasNeg := containsAny(b.TitleNorm, inversionTokens)
    return aHasNeg != bHasNeg // XOR: exactly one has negation
}

// Router correction when IsPolarityInverted == true:
// effectivePriceB = CalculateEffectivePrice(1.0 - market_b.YesPrice, ...)

2.7  Routing Engine
Orderbook Walk: CalculateWAP (orderbook.go)
// Walks asks ascending until StandardLotUSD is filled.
// Returns WAP, filled amount, and fill status.
func CalculateWAP(asks []OrderbookLevel, lotUSD float64) (wap float64, filled float64, status string) {
    remaining := lotUSD
    totalCost := 0.0

    for _, level := range asks { // asks sorted ascending
        if remaining <= 0 { break }
        take := math.Min(remaining, level.SizeUSD)
        totalCost += take * level.Price // cost at this level
        remaining -= take
    }

    filled = lotUSD - remaining
    if filled == 0 { return 0, 0, "REJECTED" }
    wap = totalCost / filled

    if remaining > 0 { return wap, filled, "PARTIAL" }
    return wap, filled, "FULL"
}

// Slippage check (applied after WAP calculation):
// if WAP > bestAsk + config.SlippageCeiling → disqualify venue

Decision Hierarchy: Route() (router.go)

STEP
CHECK
BEHAVIOR
1
Freshness
time.Since(FetchedAt) > StalenessThreshold → add to ExclusionReasons[venue] = "STALE_DATA". Log DataAgeSeconds. Continue to remaining venues.
2
Liquidity Floor
TotalDepthUSD < StandardLotUSD × 0.10 (10% floor) → add ExclusionReasons[venue] = "INSUFFICIENT_DEPTH". Partial fills are still routable.
3
Slippage Check
WAP > BestAsk + SlippageCeiling → add ExclusionReasons[venue] = "SLIPPAGE_EXCEEDED". This catches thin books where depth technically exists but is widely spread.
4
Cost (North Star)
EffectivePrice = WAP + FeeAdapter.Calculate(WAP, filled).FeePerContract. Select venue with lowest EffectivePrice. If tie: prefer higher TotalDepthUSD.
5
No Route
All venues excluded → RoutingDecision{FillStatus: "REJECTED"}. Log all ExclusionReasons. Do not panic.

2.8  Reasoning Log Output Format
The CLI must emit this format for every simulated order. This is the primary deliverable the evaluator reads.

═══════════════════════════════════════════════════════════════
  PROJECT EQUINOX — ROUTING DECISION
═══════════════════════════════════════════════════════════════
  Target Market : BTC > $100,000 by Dec 2025
  Standard Lot  : $500 USD (YES side)
  Matched Pair  : Polymarket slug=bitcoin-100k ↔ Kalshi KXBTC-25DEC31-T100000
  Confidence    : 0.84 (Title: 0.91, Date: 0.96, Category: 1.00)
  Polarity      : Aligned (no inversion)
───────────────────────────────────────────────────────────────
  VENUE         WAP      FEE/CTR   EFF.PRICE  DEPTH     STATUS
  Polymarket    0.6230   0.0039    0.6269     $2,847    FULL
  Kalshi        0.6180   0.0107    0.6287     $890      FULL
───────────────────────────────────────────────────────────────
  ✓ ROUTE TO   : POLYMARKET
  REASON       : Lower Effective Price (0.6269 vs 0.6287).
                 Kalshi quadratic fee offset Kalshi's 0.005 price advantage.
  SAVINGS      : $0.0018/contract = $0.90 on $500 lot
  DATA AGE     : Polymarket 8s | Kalshi 12s (both fresh)
───────────────────────────────────────────────────────────────
  ASSUMPTIONS  : Polymarket taker fee 1.56% peak (2026 US retail)
               : Kalshi fee_multiplier 0.07 (Series schema default)
               : Order treated as single-execution (no slippage modeled beyond WAP)
═══════════════════════════════════════════════════════════════

Part 3: Implementation Roadmap
Each layer is fully testable before the next layer is built. Done condition is explicit per step.

#
LAYER
DELIVERABLE
DONE WHEN
1
Models
All dataclasses in internal/models/market.go. Zero external imports.
File compiles. Fields match canonical spec exactly.
2
Config
All constants in internal/config/config.go. Named, documented.
go vet passes. Constants referenced by name, no magic numbers anywhere.
3
Connectors
PolymarketConnector and KalshiConnector implement VenueConnector interface. Parse fixture JSON → NormalizedMarket.
TestPriceNormalization passes: Kalshi 65¢ == Polymarket 0.65 ± 0.001.
4
Matching
NormalizeTitle(), ScorePair(), DetectInversion() implemented.
TestPolarityInversion passes. TestConfidenceThreshold passes on fixture pairs.
5
Math
CalculateWAP() and FeeAdapter impls.
TestFeeSymmetry passes. TestSlippageCeiling passes. TestPartialFill passes.
6
Router
Route() implements full hierarchy. Returns RoutingDecision with ExclusionReasons.
TestStaleExclusion passes. TestFeeOptimization passes.
7
Simulation
CLI runner: fetch → normalize → match → route → print reasoning log.
go run ./cmd/equinox produces the reasoning log format from §2.8 for at least 3 market pairs.

Part 4: Viability Test Suite
These four tests are the proof-of-viability for the final submission. Each maps directly to an architectural claim.

TEST
ASSERTION & PROOF STATEMENT
TestPriceNormalization
Assert: KalshiConnector.normalize({yes_ask_dollars: "0.6500"}).YesPrice == 0.65 within 0.001. Assert: PolymarketConnector.normalize({outcomePrices: "[\"0.65\",\"0.35\"]"}). YesPrice == 0.65. Proof: Normalization fidelity — both venues produce the same mathematical object.
TestFeeAdapterSymmetry
For both KalshiFeeAdapter and PolymarketFeeAdapter: Assert fee at p=0.50 > fee at p=0.10. Assert fee at p=0.50 > fee at p=0.90. Proof: Both quadratic fee models peak at maximum uncertainty, as expected.
TestStaleDataExclusion
Build RoutingDecision with Kalshi FetchedAt = now−61s, Polymarket FetchedAt = now−10s. Assert: SelectedVenue == POLYMARKET. Assert: ExclusionReasons[KALSHI] == "STALE_DATA". Assert: no panic, no error returned. Proof: Staleness triggers graceful exclusion, not system failure.
TestPartialFillBehavior
Build NormalizedMarket with Asks totaling only $300 (StandardLot = $500). Assert: FillStatus == "PARTIAL". Assert: WAP calculated correctly on $300. Assert: AvailableDepth == 300.0. Proof: System degrades gracefully on thin markets.
TestPolarityInversionRouting
Market A: title = "Fed raises rates" YesPrice=0.35. Market B: title = "Fed does not raise rates" YesPrice=0.65. Assert: MatchResult.IsPolarityInverted == true. Assert: Router compares MarketA.YesPrice vs (1.0 − MarketB.YesPrice) = 0.35. Proof: Inversion detection prevents logical routing errors.
TestFeeOptimization
Build scenario: Kalshi YesPrice=0.61 (cheaper), Polymarket YesPrice=0.63. Kalshi fees make EffectivePrice higher. Assert: SelectedVenue == POLYMARKET. Assert: ReasoningLog contains fee explanation string. Proof: Router optimizes on total landed cost, not raw price.

Part 5: Documentation Requirements
The PRD explicitly evaluates written explanations of equivalence and routing logic. These docs are submission deliverables, not optional.

FILE
REQUIRED CONTENT
README.md
Quick start (go run), architecture overview with layer diagram, link to all docs below, how to run tests.
docs/architecture.md
Layer diagram (ASCII or Mermaid). Data flow: API → Connector → Normalizer → Matcher → Router → CLI. Interface contracts per layer.
docs/equivalence_logic.md
Definition of market equivalence. Scoring formula with weight rationale. Alias dictionary contents and selection criteria. Polarity inversion heuristic. Confidence threshold justification. V2 upgrade path (embeddings).
docs/routing_logic.md
North Star metric definition. Decision hierarchy (Freshness > Liquidity > Cost) with rationale for ordering. Fee model formulas and assumptions. Slippage ceiling rationale. Partial fill behavior. Example routing log annotated.
docs/assumptions.md
All named constants with justification. API limitations. Known V1 constraints (binary only, no WebSocket, rule-based matching). Known edge cases not handled.

Project Equinox  ·  Final Technical Specification  ·  Go 1.21+  ·  March 2026