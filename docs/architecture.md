# Project Equinox — Architecture

## Overview

Equinox fetches live binary prediction markets from Polymarket and Kalshi, finds equivalent pairs using rule-based scoring, then routes a simulated $500 YES order to the venue with the lowest total landed cost. Output is either a formatted CLI log or a self-contained HTML report.

There are no external Go dependencies. The entire system uses the standard library only.

---

## Directory Structure

```
equinox/
├── cmd/
│   ├── equinox/main.go       # Primary CLI entry point
│   └── debug/main.go         # Debug tool: top-20 candidate pairs + token analysis
├── internal/
│   ├── config/config.go      # All numeric constants — single source of truth
│   ├── models/market.go      # Canonical data structures (venue-agnostic)
│   ├── connector/            # Venue API adapters
│   │   ├── connector.go      # VenueConnector interface
│   │   ├── polymarket.go     # Polymarket Gamma + CLOB API
│   │   ├── kalshi.go         # Kalshi REST v2 API
│   │   └── connector_test.go # Integration tests using fixture JSON
│   ├── matching/             # Market equivalence engine
│   │   ├── scorer.go         # Jaccard + date + category scoring
│   │   ├── matcher.go        # FindMatches, MatchPair, DetectInversion
│   │   └── normalize.go      # Title preprocessing pipeline
│   ├── routing/              # Order routing and fee calculation
│   │   ├── router.go         # Four-tier decision hierarchy
│   │   ├── orderbook.go      # WAP calculation (orderbook walk)
│   │   └── fees.go           # Fee adapters (Kalshi quadratic, Polymarket variable)
│   └── simulation/           # Layer orchestration and output
│       ├── runner.go         # Run, RunAndCollect, PrintReasoningLog
│       └── report.go         # Self-contained HTML report generation
├── docs/
└── testdata/                 # Fixture JSON for connector tests
```

---

## Data Flow

```
┌─────────────┐     ┌─────────────┐
│  Polymarket │     │    Kalshi   │
│  Gamma API  │     │  REST v2    │
│  CLOB API   │     │             │
└──────┬──────┘     └──────┬──────┘
       │  FetchMarkets()   │
       ▼                   ▼
┌──────────────────────────────────┐
│         connector layer          │
│  Raw JSON → NormalizedMarket[]   │
│  (prices, depth, resolution,     │
│   category, TitleNorm)           │
└──────────────────┬───────────────┘
                   │
                   ▼
┌──────────────────────────────────┐
│         matching layer           │
│  FindMatches(poly[], kalshi[])   │
│  cross-product → ScorePair()     │
│  → []MatchResult (conf ≥ 0.65)  │
└──────────────────┬───────────────┘
                   │
                   ▼
┌──────────────────────────────────┐
│         routing layer            │
│  FetchOrderbook() per match      │
│  Route() → RoutingDecision       │
│  (Freshness→Liquidity→Cost)      │
└──────────────────┬───────────────┘
                   │
                   ▼
┌──────────────────────────────────┐
│       simulation layer           │
│  PrintReasoningLog() [stdout]    │
│  GenerateReport() [report.html]  │
└──────────────────────────────────┘
```

---

## Package Responsibilities

### `config`

Single file, all numeric constants. Every threshold, weight, fee rate, and lot size is defined here. No logic.

| Constant | Value | Purpose |
|---|---|---|
| `StandardLotUSD` | `500.0` | Benchmark order size |
| `StalenessThreshold` | `60s` | Max snapshot age before venue exclusion |
| `ResolutionWindowHours` | `72.0` | Max resolution date delta (±72h window) |
| `MatchConfidenceThreshold` | `0.65` | Minimum composite score for a valid match |
| `SlippageCeiling` | `0.05` | Max WAP overshoot above best ask |
| `PolymarketPeakTakerFee` | `0.0156` | 1.56% taker fee at p=0.50 |
| `PolymarketFeeFloor` | `0.001` | 0.1% minimum taker fee |
| `KalshiFeeMultiplier` | `0.07` | Kalshi quadratic fee coefficient |
| `TitleWeight` | `0.60` | Confidence weight for title similarity |
| `DateWeight` | `0.25` | Confidence weight for date proximity |
| `CategoryWeight` | `0.15` | Confidence weight for category alignment |

---

### `models`

Venue-agnostic data structures only — no logic.

**`NormalizedMarket`** — canonical representation of a single market from either venue:
```
VenueID        string           // "KALSHI" | "POLYMARKET"
InternalID     string           // Platform-specific ID (conditionId or ticker)
TokenID        string           // Polymarket CLOB token ID (empty for Kalshi)
Title          string           // Raw market question
TitleNorm      string           // Preprocessed title (see matching/normalize.go)
YesPrice       float64          // Best ask [0.0, 1.0]
NoPrice        float64          // Complement
Asks           []OrderbookLevel // Sorted ascending by price
TotalDepthUSD  float64          // Sum of all ask levels
Category       string           // "crypto"|"economics"|"politics"|"sports"|""
ResolutionTime time.Time        // UTC resolution deadline
FetchedAt      time.Time        // Snapshot timestamp
```

**`OrderbookLevel`**:
```
Price   float64  // Normalized [0.0, 1.0]
SizeUSD float64  // Notional depth in USD at this price level
```

**`MatchResult`** — output of the matching engine for one pair:
```
MarketA, MarketB   NormalizedMarket
Confidence         float64    // TitleScore×0.60 + DateScore×0.25 + CategoryScore×0.15
TitleScore         float64    // Jaccard component
DateScore          float64    // Temporal proximity component
CategoryScore      float64    // Category alignment component
IsPolarityInverted bool       // True if YES on one side = NO on the other
Reasoning          []string   // Audit trail of scoring decisions
```

**`RoutingDecision`** — output of the router for one match:
```
SelectedVenue    string
EffectivePrice   float64            // WAP + fee/contract
WAP              float64
FeePerContract   float64
TotalCost        float64
FillStatus       string             // "FULL" | "PARTIAL" | "REJECTED"
AvailableDepth   float64            // Populated on PARTIAL
ExclusionReasons map[string]string  // e.g. {"KALSHI": "STALE_DATA"}
DataAgeSeconds   map[string]float64
ReasoningLog     []string
```

---

### `connector`

Translates raw API responses into `[]NormalizedMarket`. All venue-specific logic lives here.

**Interface:**
```go
type VenueConnector interface {
    FetchMarkets(category string) ([]NormalizedMarket, error)
    FetchOrderbook(market *NormalizedMarket) error
    VenueID() string
}
```

**Polymarket** (`polymarket.go`):
- `FetchMarkets`: iterates 14 offsets (0–1300, step 100) against `gamma-api.polymarket.com/events`
- Filters events by financial keywords: `btc`, `eth`, `fed`, `cpi`, `gdp`, `inflation`, `rate`, `bitcoin`, `ethereum`
- De-duplicates by `conditionId`
- Parses `outcomePrices` JSON string → YES/NO price pair
- `FetchOrderbook`: queries `clob.polymarket.com/book?token_id={tokenID}`, walks asks

**Kalshi** (`kalshi.go`):
- `FetchMarkets`: queries 7 hardcoded series: `KXBTC`, `KXETH`, `KXCPI`, `KXCPIYOY`, `KXFED`, `KXRATECUT`, `KXGDP`
- Endpoint: `api.elections.kalshi.com/trade-api/v2/markets?status=open&series_ticker={series}`
- `yes_ask_dollars` is already decimal (not cents); rejects degenerate prices (≤0 or ≥1)
- Title = `Title + " " + Subtitle`
- `FetchOrderbook`: `/markets/{ticker}/orderbook`, parses `orderbook_fp.yes_dollars` array

---

### `matching`

**`normalize.go` — Title preprocessing pipeline:**

`NormalizeTitle(title)` applies in order:
1. Lowercase
2. Replace `$X,XXX` patterns (strip `$` and commas)
3. Strip possessives (`'s`)
4. Expand aliases — 25+ mappings including:
   - `btc` → `bitcoin`, `eth` → `ethereum`
   - `fed` → `federal reserve`, `cpi` → `inflation`
   - Month abbreviations (jan→january, etc.), plurals, contractions
5. Remove punctuation (keep alphanumeric + space)
6. Tokenize, remove stop words (`will`, `the`, `a`, `an`, `by`, `in`, `to`, `of`, `be`, `is`, `on`, `at`)
7. Deduplicate tokens, sort alphabetically, join

This produces `TitleNorm` — stable, order-independent, alias-unified token strings for Jaccard comparison.

**`scorer.go` — `ScorePair(a, b NormalizedMarket)`:**

Returns `(titleScore, dateScore, catScore, confidence, reasoning)`.

- **TitleScore** — Jaccard similarity: `|A ∩ B| / |A ∪ B|` on token sets of `TitleNorm`
- **DateScore** — `1.0 - Δhours / (72.0 × 2)`, clamped [0, 1]; returns 0.5 if either date is zero
- **CategoryScore** — 1.0 exact match, 0.5 adjacent (crypto↔economics), 0.5 if either unknown, 0.0 mismatch
- **Confidence** — `TitleScore×0.60 + DateScore×0.25 + CategoryScore×0.15`

**`matcher.go` — `FindMatches(as, bs []NormalizedMarket)`:**

Full cross-product: every Polymarket market is scored against every Kalshi market. Pairs with `Confidence < 0.65` are discarded. Returns `[]MatchResult` (unsorted; caller sorts).

`DetectInversion(a, b)` — XOR check across negation tokens (`not`, `no`, `fail`, `cut`, `lower`, `below`, `miss`). Returns true if exactly one market contains a negation token, signaling that YES on one side semantically equals NO on the other.

---

### `routing`

**`orderbook.go` — `CalculateWAP(asks, lotUSD)`:**

Walks the ask ladder ascending, consuming depth until `lotUSD` is filled:
```
remaining = lotUSD
for each ask level:
    take = min(remaining, level.SizeUSD)
    totalCost += take × level.Price
    remaining -= take
wap = totalCost / filled
```
Returns `(wap, filled, status)` where status is `"FULL"`, `"PARTIAL"`, or `"REJECTED"`.

**`fees.go` — Fee models:**

Both models use quadratic functions that peak at p=0.50 (maximum uncertainty):

*Kalshi:*
```
feeRate = 0.07 × p × (1 − p)
feePerContract = feeRate × p   (normalized per $1 contract)
```

*Polymarket:*
```
baseRate = max(0.0156 × 4 × p × (1 − p), 0.001)
feePerContract = baseRate × p
```

**`router.go` — `Route(match MatchResult) RoutingDecision`:**

Four-tier hierarchy applied independently to each venue in the match:

```
1. FRESHNESS    — exclude if FetchedAt > 60s old           → "STALE_DATA"
2. LIQUIDITY    — exclude if TotalDepthUSD < $50           → "INSUFFICIENT_DEPTH"
3. WAP          — exclude if fill status == "REJECTED"     → no fillable asks
4. SLIPPAGE     — exclude if WAP > bestAsk + 0.05          → "SLIPPAGE_EXCEEDED"
5. COST         — effectivePrice = WAP + feePerContract    → select lowest
```

If both venues survive: select lowest `effectivePrice`; tie-break on higher `TotalDepthUSD`.

Special cases:
- **Polarity inversion:** if `IsPolarityInverted` and the market is `MarketB`, use `1.0 − YesPrice`
- **Missing orderbook:** if `Asks` is empty (fetch failed), synthesize `{Price: YesPrice, SizeUSD: $500}`

---

### `simulation`

**`runner.go`:**

`runCore()` orchestrates the full pipeline:
1. `FetchMarkets` from both venues
2. `FindMatches` → sort by confidence descending
3. `FetchOrderbook` + `Route` for up to `maxRoute=18` matches
4. Print top-`topN=5` reasoning logs to stdout (deduped by Polymarket title to avoid duplicate display)
5. Return all results for optional report generation

Two public entry points:
- `Run(poly, kalshi)` — CLI mode, stdout only
- `RunAndCollect(poly, kalshi)` — returns decisions + matches for report generation

**`report.go` — `GenerateReport(...) ([]byte, error)`:**

Produces a single self-contained HTML file. No external requests on load (all CSS, JS, and data embedded).

Five sections:

| Section | Content |
|---|---|
| 0 — Market Explorer | Interactive dual-column market search; click any market to see its top-8 candidate matches with confidence bars, score breakdowns, polarity badges, and links to routing cards |
| 1 — Run Summary | Four metric cards: Polymarket count, Kalshi count, matched pairs, timestamp |
| 2 — Matched Pairs Table | Collapsible rows for all matched pairs; columns: title, confidence, resolution date, scores, polarity, selected venue, effective price |
| 3 — Top-5 Routing Cards | Full decision card per match: venue panels side-by-side, reasoning trail with step annotations, data age indicators |
| 4 — Assumptions & Audit | Active configuration table + matching reasoning logs for top-5 |
| 5 — Known Limitations | V1 false-positive risk and V2 mitigation notes |

---

## CLI Entry Points

**`cmd/equinox/main.go`**

```
go run ./cmd/equinox              # Fetch, match, route → print top-5 to stdout
go run ./cmd/equinox --report     # Same + write report.html
go run ./cmd/equinox --show-misses  # Print near-miss pairs (0.40 ≤ conf < 0.65)
```

**`cmd/debug/main.go`**

```
go run ./cmd/debug                # Top-20 candidate pairs, token intersection analysis
```

---

## External APIs

| Venue | Endpoint | Usage |
|---|---|---|
| Polymarket | `gamma-api.polymarket.com/events` | Market metadata, prices, resolution dates |
| Polymarket | `clob.polymarket.com/book` | Live orderbook (asks) |
| Kalshi | `api.elections.kalshi.com/trade-api/v2/markets` | Market metadata by series |
| Kalshi | `api.elections.kalshi.com/trade-api/v2/markets/{ticker}/orderbook` | Live orderbook |

HTTP client timeout: 15s for both venues.

---

## Known V1 Limitations

- **False positives near threshold (0.65–0.72):** Markets sharing surface tokens (e.g., both mentioning "fed" and a similar date) can score above threshold despite referring to different events. V2 mitigation: price-proximity sanity check — reject matches where venue prices diverge by more than 50 percentage points.
- **No semantic distance:** Jaccard cannot distinguish "rate increase" from "rate decrease"; polarity inversion heuristic partially compensates but relies on negation token presence.
- **Hardcoded Kalshi series:** Only 7 series are queried. New Kalshi product lines require code changes.
- **Single-execution assumption:** Routing simulates the full lot as one order. Partial fill cost curves are not modeled.
