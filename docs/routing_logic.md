# Routing Logic

## North Star Metric

The routing system optimizes a single number: **Effective Price for a $500 Standard Lot**.

```
EffectivePrice = WAP + FeePerContract
```

Where:
- **WAP** (Weighted Average Price) is the probability-denominated cost per contract after walking the orderbook for $500 notional
- **FeePerContract** is the venue's transaction fee amortized across the contracts filled

This metric is the "total landed cost" of entering a YES position on the matched market. Raw price alone is an insufficient comparison — Kalshi's quadratic fee structure means a venue that quotes 0.2¢ cheaper per contract can actually be more expensive to trade on after fees, depending on the probability. The North Star metric catches this.

---

## Decision Hierarchy

The `Route()` function in `internal/routing/router.go` implements a strict sequential gating hierarchy. A venue must pass each gate before reaching the next.

### Step 1 — Freshness Check

```
time.Since(m.FetchedAt) > StalenessThreshold (60s) → ExclusionReasons[venue] = "STALE_DATA"
```

Freshness is evaluated first because stale price data invalidates all subsequent math. A venue that was last snapshotted 90 seconds ago may have moved substantially — applying fee calculations to its prices would produce a misleading effective price that doesn't reflect what's actually available in the market. It is better to exclude a venue and route to the other than to execute against a ghost quote.

Stale venues are **excluded, not errored**. The system logs the data age in seconds (`DataAgeSeconds[venue]`) and continues evaluating remaining venues. This graceful degradation is a deliberate design choice: a brief API hiccup on one venue should not prevent routing to the other.

### Step 2 — Liquidity Floor

```
TotalDepthUSD < StandardLotUSD × 0.10 → ExclusionReasons[venue] = "INSUFFICIENT_DEPTH"
```

The liquidity floor is set at 10% of the standard lot ($50). A venue with less than $50 of visible depth provides no meaningful price discovery and is excluded. Partial fills are still routable — the floor only gates venues where depth is negligible. This is intentionally lenient: even a thin market with $60 of depth clears the floor and may receive a PARTIAL fill designation, which the caller can inspect.

### Step 3 — Slippage Check

```
WAP > BestAsk + SlippageCeiling (0.05) → ExclusionReasons[venue] = "SLIPPAGE_EXCEEDED"
```

After the orderbook walk, slippage is measured as the difference between the WAP and the best available ask. The 5% absolute probability ceiling (`config.SlippageCeiling`) gates out markets where depth technically exists but is spread so widely that execution would occur at prices meaningfully worse than the top of book. On a binary outcome priced at 0.65, a 5-point slippage ceiling means the WAP must not exceed 0.70. This check catches cases the liquidity floor misses: a market might have $600 depth, but if it's spread across $100 at 0.65 and $500 at 0.85, the WAP on a $500 lot is unacceptable.

The slippage ceiling is denominated in absolute probability points rather than percentage — this is correct for binary prediction markets, where the probability itself is the price.

### Step 4 — Cost (The North Star)

Among venues that pass all three gates, the router selects the one with the lowest **EffectivePrice**. If two venues produce identical effective prices (a very rare tie), the router prefers the one with higher `TotalDepthUSD` — more depth means the decision is more robust to incremental size.

If no venues pass the gates, the decision returns `FillStatus: "REJECTED"` with all exclusion reasons logged. The function never panics.

---

## Fee Model Formulas

### Kalshi — Quadratic Fee Model

```
contracts = tradeValueUSD / price
fee = 0.07 × contracts × price × (1 − price)
FeePerContract = fee / contracts = 0.07 × price × (1 − price)
```

The `0.07` coefficient (`config.KalshiFeeMultiplier`) is Kalshi's published Series schema default. The quadratic structure means fees peak at maximum uncertainty (p = 0.50) and approach zero at certainty (p → 0 or p → 1). At p = 0.50, `FeePerContract ≈ 0.0175`. At p = 0.90, `FeePerContract ≈ 0.0063`.

The quadratic fee makes Kalshi relatively more expensive for markets with genuine uncertainty and relatively cheaper for near-certain markets.

### Polymarket — Variable Taker Fee with Floor

```
BaseRate = max(1.56% × 4 × price × (1 − price), 0.10%)
TotalFee = tradeValueUSD × BaseRate
FeePerContract = TotalFee / contracts
```

The `1.56%` peak taker fee (`config.PolymarketPeakTakerFee`) is drawn from Polymarket's published 2026 US retail taker schedule. The formula `1.56% × 4 × p × (1−p)` produces a quadratic that peaks at exactly 1.56% when p = 0.50. The 0.1% floor (`config.PolymarketFeeFloor`) prevents near-zero fees at probability extremes and ensures Polymarket always collects some minimal taker spread.

Both fee adapters implement the `FeeAdapter` interface and return a `FeeEstimate` struct that includes `Assumptions []string` — these flow directly into the routing decision reasoning log, ensuring every fee number is traceable to a documented assumption.

---

## Slippage Ceiling Rationale

Five percent absolute probability (0.05) was chosen as a ceiling that:
1. Is tight enough to exclude genuinely thin markets with wide spread
2. Is loose enough to tolerate natural orderbook spread in normal-depth markets
3. Corresponds to a real dollar impact on a $500 lot: 5 probability points × ~770 contracts ≈ $38.50 of incremental cost vs. best ask

Markets where the WAP exceeds best ask by more than 5 points are typically shallow enough that any meaningful fill would move price substantially. In such cases, the standard lot assumption breaks down and the comparison against the other venue is no longer apples-to-apples.

---

## Partial Fill Behavior

When an orderbook has less total depth than the $500 standard lot but more than the $50 liquidity floor, the router records the fill as `PARTIAL`. The WAP is calculated on the available depth only, and `AvailableDepth` in the routing decision reports the actual filled notional.

This design treats partial execution as a valid routing outcome rather than a failure. In practice, a $300 partial fill at 0.63 may still be preferable to a $500 full fill at 0.65 on the competing venue. The caller (simulation runner) logs the partial status so the user understands they would not achieve full size.

---

## Polarity Inversion Correction in the Router

When `MatchResult.IsPolarityInverted == true`, the router adjusts the price used for Market B:

```go
if match.IsPolarityInverted && m.VenueID == match.MarketB.VenueID {
    price = 1.0 - m.YesPrice
}
```

This correction ensures the router compares like-for-like: both venues' prices now represent the cost of expressing the same directional view. The inversion flag is set by the matcher (never by the router), preserving clean layer separation.

---

## Example Routing Log — Annotated

```
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
  Polymarket    0.6230   0.0039    0.6269     $2,847    FULL    ← lower eff. price wins
  Kalshi        0.6180   0.0107    0.6287     $890      FULL    ← cheaper WAP, costlier fee
───────────────────────────────────────────────────────────────
  ✓ ROUTE TO   : POLYMARKET
  REASON       : Lower Effective Price (0.6269 vs 0.6287).
                 Kalshi quadratic fee offset Kalshi's 0.005 price advantage.
  SAVINGS      : $0.0018/contract = $0.90 on $500 lot
  DATA AGE     : Polymarket 8s | Kalshi 12s (both fresh)
───────────────────────────────────────────────────────────────
  ASSUMPTIONS  : Polymarket taker fee 1.56% peak (2026 US retail)
               : Kalshi fee_multiplier=0.07 (Series schema default)
               : Order treated as single-execution (no slippage modeled beyond WAP)
═══════════════════════════════════════════════════════════════
```

**Key observations from this example:**
- Kalshi has a 0.5¢ lower WAP (0.6180 vs 0.6230) — raw price favors Kalshi
- Kalshi's quadratic fee at p ≈ 0.62 is `0.07 × 0.62 × 0.38 ≈ 0.0165/contract`, while Polymarket's variable fee is approximately `0.0063/contract` — fee difference is 1.02¢/contract
- Net result: Polymarket is 0.18¢/contract cheaper on a total-landed-cost basis
- On a $500 lot at ~770 contracts, the savings is ~$0.90 — small in absolute terms but directionally significant and directionally correct

This is exactly the scenario the North Star metric is designed to surface: a venue with a worse raw price wins on total cost because of fee structure.
