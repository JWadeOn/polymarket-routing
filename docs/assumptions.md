# Assumptions

This document records every named constant, API limitation, V1 scope constraint, and known unhandled edge case in Project Equinox. All constants referenced below are defined in `internal/config/config.go` as named Go constants — no magic numbers appear elsewhere in the codebase.

---

## Named Constants

### Order Sizing

| Constant | Value | Justification |
|---|---|---|
| `StandardLotUSD` | `500.0` | The North Star metric is defined for this lot size. $500 is large enough to reveal real slippage on thin orderbooks, small enough to be filled in normal-depth markets. It is a single evaluator-facing benchmark, not a production order size. |

### Data Freshness

| Constant | Value | Justification |
|---|---|---|
| `StalenessThreshold` | `60s` | Both APIs are polled on snapshot basis (no WebSocket). A 60-second TTL is the tightest practical window at which stale-data exclusions do not become so frequent that no venue is ever eligible. Shorter windows (30s) cause excessive exclusions on slow-polling callers; longer windows (120s) allow meaningfully outdated prices to influence routing. |

### Matching Parameters

| Constant | Value | Justification |
|---|---|---|
| `ResolutionWindowHours` | `72.0` | The decay window for date scoring. Oracle delays, timezone normalization differences, and "end of day" interpretation variance between venues commonly produce 24–48 hour resolution time discrepancies for the same real event. A 72-hour window provides comfortable margin without allowing weeks-apart events to match. |
| `MatchConfidenceThreshold` | `0.65` | Minimum composite score to declare a match. At 0.65, near-certain title overlap (Jaccard ≥ 0.80) combined with date proximity produces a valid match. Below 0.60, high-frequency tokens shared across unrelated financial markets ("rate", "2025", "will") generate false positives at unacceptable rates on real API data. |

### Matching Weights

| Constant | Value | Justification |
|---|---|---|
| `TitleWeight` | `0.60` | Title is the primary semantic signal. Two markets about different topics almost always have low title overlap. |
| `DateWeight` | `0.25` | Date mismatch is the second-strongest differentiator, particularly for recurring events (monthly reports, quarterly earnings). |
| `CategoryWeight` | `0.15` | Category is a useful tie-breaker but is applied inconsistently across venues. It cannot be load-bearing because the same event may be tagged differently on each platform. |

Note: `TitleWeight + DateWeight + CategoryWeight = 1.0` — this is enforced by inspection and documented here as a constraint on any future weight modifications.

### Routing Constraints

| Constant | Value | Justification |
|---|---|---|
| `SlippageCeiling` | `0.05` | Five absolute probability points represents approximately $38.50 of incremental execution cost on a $500 lot at typical probabilities (~770 contracts). Beyond 5 points, the WAP is distorted enough to make the routing comparison unreliable. |

### Fee Model Parameters

| Constant | Value | Justification |
|---|---|---|
| `PolymarketPeakTakerFee` | `0.0156` | Polymarket's published 2026 US retail taker schedule. Peak fee applies at maximum uncertainty (p = 0.50). This is a public schedule; actual fees may differ for institutional accounts or with different market maker arrangements. |
| `PolymarketFeeFloor` | `0.001` | Polymarket's published 0.1% minimum. Prevents the fee model from returning zero (or near-zero) at probability extremes, which would artificially favor near-certain markets on Polymarket. |
| `KalshiFeeMultiplier` | `0.07` | Kalshi's Series schema default fee coefficient. Kalshi fees are defined per-series and may vary; 0.07 is the default applied when no series-specific override is available. Production systems should fetch the actual fee multiplier per series from the API. |

---

## API Limitations

### Polymarket

- **CLOB orderbook requires token ID, not condition ID.** `FetchOrderbook` uses the market's `condition_id` as a fallback for the token ID. For some markets this returns an empty orderbook. In those cases the router synthesizes a single-level book from `YesPrice` and `TotalDepthUSD`. This approximation is correct for cost comparison purposes but produces a WAP equal to the best ask (no slippage model) and a fill status that may be FULL even when the real orderbook would produce a PARTIAL fill.

- **Gamma API returns `outcomePrices` as a JSON-encoded string** (`"[\"0.65\",\"0.35\"]"`), not a native JSON array. The connector must unmarshal this string before extracting the YES price. This is an undocumented API quirk.

- **No authenticated endpoints used.** Unauthenticated public API access is subject to rate limiting and may not return all available markets. Production use would require an API key.

### Kalshi

- **`yes_ask_dollars` is already a decimal probability** (not cents). The field name is misleading — despite "dollars" in the name, the value `"0.65"` means the ask is $0.65 per contract (i.e., 65 cents), which in Kalshi's contract model represents a 65% probability. No division by 100 is needed. This is the documented behavior as of the v2 API.

- **Orderbook levels are returned as `[[price_cents, quantity], ...]`** — a nested array format. The connector converts: `price = price_cents / 100.0`, `sizeUSD = quantity × (price_cents / 100.0)`. This approximation uses contract quantity × price as a proxy for notional USD, which slightly underestimates dollar depth at low probabilities.

- **Kalshi's `close_time` field** is in RFC3339 format with timezone offset. The connector parses this to UTC. Kalshi markets may be created with local-time close times that shift slightly when normalized to UTC, contributing to the observed 24–48 hour resolution-time delta justifying the 72-hour window.

---

## V1 Scope Constraints

These are intentional limitations of the V1 implementation, not bugs.

**Binary markets only.** The system exclusively processes YES/NO binary markets. Categorical markets (multiple outcomes) and scalar markets (numeric ranges) are not supported. The `NormalizedMarket` struct has no fields for multi-outcome probability distributions. Adding categorical support would require a new matching strategy for outcome alignment, a new routing metric (probability × outcome rather than simple YesPrice), and new connector logic.

**No WebSocket feeds.** All price data is obtained via REST API snapshots. The 60-second staleness TTL is a consequence of this polling architecture. Real-time routing would require persistent WebSocket connections to both venues.

**Rule-based matching only.** The matcher uses Jaccard token similarity, not semantic embeddings. Paraphrased titles that share no tokens can produce low confidence scores despite representing the same event. The V2 embedding-based upgrade path is documented in `docs/equivalence_logic.md`.

**No persistence.** All results are ephemeral. The simulation runner prints to stdout. No database, no file output by default. Optional JSON redirection (`go run ./cmd/equinox > results.json`) can produce a file but there is no structured output format defined.

**Single execution model.** The router assumes the entire $500 lot is filled in one market order against the current orderbook snapshot. It does not model TWAP, VWAP over time, or the market impact of a large order.

---

## Known Edge Cases Not Handled

**Same-venue pair**: `FindMatches` takes two slices (one per venue). If the same venue's markets appear in both slices, the system may match a market against itself and route to it trivially. The simulation runner prevents this by passing Polymarket and Kalshi slices separately — but the matcher itself has no guard.

**Zero-probability markets**: A market where `YesPrice = 0.0` or `YesPrice = 1.0` has undefined orderbook behavior in both fee models (division by price in the Kalshi model). The connector normalizes these to a safe range, but no explicit guard exists in the fee adapters.

**Resolution time in the past**: Expired markets may still appear in API responses before the platform archives them. The connector does not filter by resolution time. A market that resolved yesterday could theoretically be matched and routed against a currently-active market.

**Title normalization collision**: Two genuinely different markets could normalize to the same `TitleNorm` if they share all meaningful tokens (e.g., two markets asking "Will Bitcoin exceed $100,000?" with different resolution dates). In this case, date scoring becomes the sole discriminator. If dates are also similar, both markets might match the same counterpart.

**Tie-breaking by depth**: When two venues produce identical effective prices, the router selects the one with higher `TotalDepthUSD`. In practice this tie should be rare; in theory, floating-point precision differences in fee calculations will almost always produce a non-zero price differential before reaching this branch.

---

## Known V1 Matcher Limitations

Marginal matches near the confidence threshold (0.65–0.72) may produce false positives where semantically unrelated markets share enough surface-level tokens to score above threshold. Example observed in live data: "Fed abolished before 2027?" (Polymarket) matched to KXRATECUT-26DEC31 (Kalshi) at 0.69 confidence due to shared "fed" and date proximity tokens. A price-proximity sanity check (flag matches where venue prices diverge >50 percentage points) is the recommended V2 mitigation.
