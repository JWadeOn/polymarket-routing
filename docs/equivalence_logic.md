# Equivalence Logic

## What "Equivalent" Means

Two binary prediction markets from different venues are considered **equivalent** when they represent the same real-world outcome resolved at approximately the same time. Equivalence is not identity — venues phrase questions differently, use different abbreviations, and may disagree on exact resolution dates by hours or days due to timezone handling. The matcher must be tolerant of these surface differences while still correctly rejecting markets that happen to share similar words but describe different events.

A match is declared when the composite confidence score reaches or exceeds **0.65** (`config.MatchConfidenceThreshold`). Below that threshold, the pair is discarded regardless of any individual component score.

---

## Scoring Formula

The composite confidence score is a weighted sum of three orthogonal signals:

```
Confidence = (TitleScore × 0.60) + (DateScore × 0.25) + (CategoryScore × 0.15)
```

Each component is computed independently, which means the audit trail in `MatchResult.Reasoning` can surface exactly which dimension caused a pair to be accepted or rejected. The weights are named constants in `internal/config/config.go` — not magic numbers — so they can be tuned and cited.

### Title Score (weight: 0.60)

**Formula:** Jaccard similarity on token sets — `|A∩B| / |A∪B|`

Title is given the highest weight because it is the primary semantic signal. Two markets about different topics almost always have non-overlapping titles, making title score the fastest discriminator. Jaccard is used rather than edit distance or substring matching because prediction market titles are short, unordered noun phrases ("Bitcoin above 100k by December") and token order carries little information. Jaccard naturally handles reorderings and partial overlaps.

Title scoring is applied to the **normalized** form (`TitleNorm`), not the raw display title. This is critical: without normalization, "BTC > $100,000" and "Bitcoin above 100000" score near zero despite being the same event.

### Date Score (weight: 0.25)

**Formula:** `max(0, 1 − delta / (72h × 2))`

Where `delta` is the absolute difference in resolution times in hours. The score decays linearly from 1.0 (identical resolution time) to 0.0 at 144 hours (six days apart). At exactly 72 hours delta, the score is 0.5.

Date is the second-most-important signal because the same recurring event (e.g., a monthly CPI release) will appear across multiple instances on each venue. Without date scoring, a Polymarket market for "CPI above 3% in March" could match a Kalshi market for "CPI above 3% in April" — they share the same title but are entirely different bets.

The 72-hour window (`config.ResolutionWindowHours`) accommodates real observed behavior: venues frequently disagree on resolution time by up to 48 hours due to oracle delays, timezone normalization differences (UTC vs. local), and differences in how "end of day" is interpreted. Choosing 72h over, say, 24h reduces false negatives substantially on real API data.

If either market has a missing (`zero`) resolution time, the date score defaults to **0.5** (neutral) rather than 0.0 (penalizing) or 1.0 (rewarding). This is a deliberate "benefit of the doubt" for markets where one venue does not surface a close time in the API response.

### Category Score (weight: 0.15)

**Values:** 1.0 (exact match), 0.5 (adjacent categories), 0.0 (no overlap)

Category is the weakest signal because it is derived from coarse venue-supplied tags that are inconsistently applied. Two equivalent crypto markets might be tagged "crypto" on one venue and "economics" on another. The adjacency map (`crypto ↔ economics`) handles the most common case of this kind of disagreement.

Category score is given weight 0.15 rather than being a hard filter. A hard filter based on category would produce false negatives on real data; a small continuous weight provides a mild tie-breaking nudge without dominating.

---

## Title Normalization Pipeline

Before Jaccard scoring, both titles pass through `NormalizeTitle()` in `internal/matching/normalize.go`. The pipeline, in order:

1. **Lowercase** — eliminates case variation
2. **Dollar-sign stripping** — `$100,000` → `100000` (regex `\$[\d,]+`)
3. **Alias expansion** — replaces known abbreviations with canonical forms (see table below)
4. **Punctuation removal** — strips all non-alphanumeric, non-space characters
5. **Stop word removal** — removes high-frequency tokens with no discriminating power
6. **Token deduplication** — each token appears at most once in the set
7. **Alphabetical sort** — order-independent comparison; `"bitcoin raise"` and `"raise bitcoin"` produce the same normalized form

### Alias Dictionary

| Abbreviation | Expansion | Rationale |
|---|---|---|
| `btc` | `bitcoin` | Polymarket uses "BTC", Kalshi uses "Bitcoin" |
| `eth` | `ethereum` | Same cross-venue inconsistency as BTC |
| `fed` | `federal reserve` | Common shorthand in financial markets |
| `cpi` | `inflation` | CPI is the most common inflation market trigger |
| `usd` | `dollar` | Cross-venue price reference normalization |
| `100k` | `100000` | Numeric threshold alignment |
| `1m` | `1000000` | Same |
| `q1`–`q4` | `first quarter`–`fourth quarter` | Fiscal quarter alignment |

### Stop Words

`will`, `the`, `a`, `an`, `by`, `in`, `to`, `of`, `be`, `is`, `on`, `at`

These words appear in nearly every prediction market title and carry no distinguishing power. Removing them improves Jaccard scores for pairs that differ only in phrasing connectives (e.g., "Will X be above Y by Z?" vs "X above Y on Z").

---

## Polarity Inversion Detection

Some market pairs are semantically equivalent but directionally inverted — one venue offers "Fed raises rates" while the other offers "Fed does not raise rates." A YES bet on the first is equivalent to a NO bet on the second.

Inversion is detected after confidence scoring via an XOR check on negation tokens:

```go
var inversionTokens = []string{"not", "no", "fail", "cut", "lower", "below", "miss"}

func DetectInversion(a, b NormalizedMarket) bool {
    aHasNeg := containsAny(a.TitleNorm, inversionTokens)
    bHasNeg := containsAny(b.TitleNorm, inversionTokens)
    return aHasNeg != bHasNeg  // XOR: exactly one has negation
}
```

Inversion is only checked when `Confidence ≥ MatchConfidenceThreshold`. If detected, the router corrects by using `1.0 − MarketB.YesPrice` as the effective comparison price for Market B — because a YES on the inverted market corresponds to the opposing outcome.

The XOR approach is intentional: if both titles contain negation words, they may be equivalent without inversion (e.g., "Bitcoin will not fall below 50k" vs "Bitcoin won't drop under 50k"). If neither contains negation, they are aligned. Only when exactly one has a negation token is inversion declared.

---

## Confidence Threshold Justification

The threshold of **0.65** was selected based on the following logic:

- A perfect title match (Jaccard = 1.0), identical date, and identical category yields Confidence = 1.0.
- A strong title match (0.80), same date, and unknown category yields approximately 0.68 — just above threshold.
- Two markets with unrelated titles (Jaccard ≈ 0.15) but identical date and category cannot exceed ~0.47 — well below threshold.
- At 0.65, false positives are rare on real API data (cross-topic matches don't typically score this high). Below 0.60, high-frequency words like "2025" and "rate" cause spurious matches between unrelated markets.

The threshold is a named constant so it can be adjusted without grep-and-replace refactoring.

---

## V2 Upgrade Path: Semantic Embeddings

The rule-based Jaccard approach has one well-understood failure mode: paraphrases that share no tokens. "Will the Federal Reserve cut interest rates?" and "Fed to reduce borrowing costs?" may refer to the same event but produce low Jaccard scores.

The V2 upgrade replaces (or augments) `NormalizeTitle` + `jaccardSimilarity` with a vector similarity check using sentence embeddings (e.g., via the Claude API or a locally-deployed model). The `MatchResult.Reasoning` field is already structured to accept arbitrary explanation strings, so the interface contract between the matcher and the router does not change. The `FetchMarkets`/`FetchOrderbook` connector interface is similarly unaffected. Only `scorer.go` and `normalize.go` require modification.

The PRD explicitly prioritizes explainability over raw accuracy for V1, which is why embedding-based matching was deferred.
