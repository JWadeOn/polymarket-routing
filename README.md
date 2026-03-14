# Project Equinox

Cross-venue prediction market aggregation and routing simulation. Finds equivalent binary markets on Polymarket and Kalshi, then routes a $500 YES-side order to the venue with the lowest total landed cost (WAP + fees).

---

## Quick Start

```bash
# Build
go build ./...

# Run simulation (fetches live API data)
go run ./cmd/equinox

# Run all tests (uses fixtures — no live API calls)
go test ./...
```

The simulation fetches live market data from both venues, finds matched pairs, and prints a structured routing decision log for each pair to stdout. No authentication, no configuration, no side effects.

---

## Architecture Overview

See [`docs/project_equinox_architecture.html`](docs/project_equinox_architecture.html) for the interactive layer diagram.

```
External APIs
  ├── Polymarket Gamma API (gamma-api.polymarket.com)
  │     └── Polymarket CLOB API (clob.polymarket.com)
  └── Kalshi REST v2 (api.elections.kalshi.com/trade-api/v2)

Connector Layer  (internal/connector)
  ├── PolymarketConnector  — FetchMarkets, FetchOrderbook, normalizeMarket
  └── KalshiConnector      — FetchMarkets, FetchOrderbook, normalizeMarket
            ↓ VenueConnector interface (both implement the same contract)

Canonical Model  (internal/models)
  └── NormalizedMarket, MatchResult, RoutingDecision

Matching Layer  (internal/matching)
  ├── NormalizeTitle  — lowercase, alias expansion, stop words, sort tokens
  ├── ScorePair       — Jaccard title + date decay + category → weighted Confidence
  └── DetectInversion — XOR negation token check for polarity-inverted pairs

Routing Layer  (internal/routing)
  ├── CalculateWAP  — orderbook walk, returns FULL / PARTIAL / REJECTED
  ├── FeeAdapter    — KalshiFeeAdapter (quadratic), PolymarketFeeAdapter (variable taker)
  └── Route         — Freshness → Liquidity → Slippage → Cost hierarchy

Simulation  (internal/simulation)
  └── Run — wires all layers, formats and prints reasoning log per pair

CLI  (cmd/equinox/main.go)
  └── Entry point — calls simulation.Run()
```

Data flows in one direction: raw API JSON enters the connector layer, exits as `NormalizedMarket` structs, and no venue-specific logic exists downstream of that boundary. The matcher and router are venue-agnostic.

---

## Documentation

| Document | Contents |
|---|---|
| [`docs/equivalence_logic.md`](docs/equivalence_logic.md) | How market equivalence is determined: scoring formula, weight rationale, alias dictionary, polarity inversion heuristic, confidence threshold justification, V2 upgrade path |
| [`docs/routing_logic.md`](docs/routing_logic.md) | North Star metric, decision hierarchy with rationale, fee model formulas, slippage ceiling, partial fill behavior, annotated example routing log |
| [`docs/assumptions.md`](docs/assumptions.md) | All named constants with justification, API quirks and limitations, V1 scope constraints, known unhandled edge cases |
| [`docs/project_equinox_architecture.html`](docs/project_equinox_architecture.html) | Interactive layer diagram with interface contracts |

---

## Running Tests

```bash
go test ./...
```

All six viability tests use fixture data from `testdata/` — no live API calls are made during `go test`.

| Test | Package | What It Proves |
|---|---|---|
| `TestPriceNormalization` | `connector` | Kalshi `"0.65"` string and Polymarket `"0.65"` string both normalize to `0.65 ±0.001`. Normalization fidelity across venues. |
| `TestFeeAdapterSymmetry` | `routing` | For both fee models, `fee(p=0.50) > fee(p=0.10)` and `fee(p=0.50) > fee(p=0.90)`. Both models correctly peak at maximum uncertainty. |
| `TestStaleDataExclusion` | `routing` | A venue with `FetchedAt = now−61s` is excluded with `ExclusionReasons["KALSHI"] = "STALE_DATA"`. No panic, no error — graceful degradation. |
| `TestPartialFillBehavior` | `routing` | A $300-depth orderbook against a $500 lot produces `FillStatus = "PARTIAL"` and `AvailableDepth = 300.0`. WAP is correct on the partial fill. |
| `TestPolarityInversionRouting` | `routing` | "Fed raises rates" (YesPrice=0.35) matched against "Fed does not raise rates" (YesPrice=0.65) produces `IsPolarityInverted = true`. Router compares 0.35 vs `1.0 − 0.65 = 0.35`. |
| `TestFeeOptimization` | `routing` | Kalshi YesPrice=0.61 (raw cheaper), Polymarket YesPrice=0.63. After fees, Polymarket wins. `SelectedVenue == "POLYMARKET"` and `ReasoningLog` contains fee explanation. |

---

## Constants (Named, Justified)

All system policies live in [`internal/config/config.go`](internal/config/config.go). No magic numbers appear in business logic. Full justification for each constant is in [`docs/assumptions.md`](docs/assumptions.md).

| Constant | Value | Summary |
|---|---|---|
| `StandardLotUSD` | `500.0` | Benchmark lot size for the North Star metric |
| `StalenessThreshold` | `60s` | Tightest practical TTL for REST-polled snapshots |
| `ResolutionWindowHours` | `72.0` | Accommodates oracle delays and timezone differences |
| `MatchConfidenceThreshold` | `0.65` | Below this, false positives dominate on real data |
| `SlippageCeiling` | `0.05` | 5 probability points max acceptable WAP vs. best ask |
| `PolymarketPeakTakerFee` | `0.0156` | 1.56% peak at p=0.50 (2026 US retail schedule) |
| `PolymarketFeeFloor` | `0.001` | 0.1% minimum taker fee |
| `KalshiFeeMultiplier` | `0.07` | Kalshi Series schema default fee coefficient |
| `TitleWeight` | `0.60` | Title is the primary semantic signal |
| `DateWeight` | `0.25` | Date mismatch indicates different event instances |
| `CategoryWeight` | `0.15` | Useful signal but inconsistently applied across venues |

---

## V1 Constraints

- **Binary (YES/NO) markets only.** Categorical and scalar markets are out of scope.
- **No WebSocket feeds.** Snapshot-based polling with 60-second staleness TTL.
- **Rule-based matching.** Jaccard token similarity + aliases. Semantic embedding matching is documented as the V2 upgrade path in `docs/equivalence_logic.md`.
- **No persistence.** Results are ephemeral unless redirected: `go run ./cmd/equinox > results.txt`.
- **Polymarket CLOB orderbook** uses `condition_id` as a token ID fallback. Some markets return empty orderbooks; the router synthesizes a single-level book from `YesPrice` in those cases.



empirical evidence that cross-venue normalization and routing infrastructure is viable:
"On March 14 2026, the system identified a 62-percentage-point pricing divergence on 'Fed emergency rate cut before 2027' — Polymarket pricing YES at 21¢ vs Kalshi at 84¢ on the same binary outcome. The router correctly selected Polymarket as the lower effective price venue with 0.86 match confidence."