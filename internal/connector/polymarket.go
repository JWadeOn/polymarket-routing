package connector

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jwadeon/equinox/internal/matching"
	"github.com/jwadeon/equinox/internal/models"
)

const (
	polymarketGammaBase = "https://gamma-api.polymarket.com"
	polymarketCLOBBase  = "https://clob.polymarket.com"
)

// PolymarketConnector implements VenueConnector for the Polymarket Gamma + CLOB APIs.
type PolymarketConnector struct {
	client *http.Client
}

// NewPolymarketConnector returns a PolymarketConnector with a default HTTP client.
func NewPolymarketConnector() *PolymarketConnector {
	return &PolymarketConnector{
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *PolymarketConnector) VenueID() string { return "POLYMARKET" }

// gammaMarket is the raw shape returned by the Gamma API /markets endpoint.
type gammaMarket struct {
	ConditionID   string `json:"conditionId"`
	Slug          string `json:"slug"`
	Question      string `json:"question"`
	Description   string `json:"description"`
	OutcomePrices string `json:"outcomePrices"` // JSON string e.g. "[\"0.18\",\"0.82\"]"
	EndDate       string `json:"endDate"`       // ISO-8601 e.g. "2026-03-31T12:00:00Z"
	Active        bool   `json:"active"`
	Closed        bool   `json:"closed"`
	Events        []struct {
		Title    string `json:"title"`
		Category string `json:"category"`
	} `json:"events"`
	ClobTokenIds string `json:"clobTokenIds"` // JSON-encoded string array
}

// polymarketFinKeywords are topic keywords used to filter Polymarket events client-side.
// The Polymarket Gamma API tag= and category= filters are non-functional on the public API.
var polymarketFinKeywords = []string{
	"bitcoin", "btc", "ethereum", "eth", "crypto",
	"fed", "federal reserve", "rate cut", "interest rate",
	"inflation", "cpi", "gdp", "recession",
}

// polymarketFetchOffsets is the list of event-page offsets to scan.
// Financial markets are spread across pages; scanning pages 0-1200 covers all active ones.
var polymarketFetchOffsets = []int{0, 100, 200, 300, 400, 500, 600, 700, 800, 900, 1000, 1100, 1200}

// FetchMarkets fetches active financial and crypto markets from Polymarket Gamma API.
// Scans multiple event pages and retains only markets whose question contains a
// financial or crypto keyword. Deduplicates by conditionId before normalizing.
// (The Polymarket Gamma API tag= filter is non-functional on the public API.)
func (p *PolymarketConnector) FetchMarkets(category string) ([]models.NormalizedMarket, error) {
	seen := map[string]bool{}
	var raw []gammaMarket

	for _, offset := range polymarketFetchOffsets {
		url := fmt.Sprintf("%s/events?limit=100&active=true&closed=false&offset=%d", polymarketGammaBase, offset)
		resp, err := p.client.Get(url)
		if err != nil {
			log.Printf("polymarket fetch offset=%d: %v", offset, err)
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("polymarket read offset=%d: %v", offset, err)
			continue
		}
		var events []gammaEvent
		if err := json.Unmarshal(body, &events); err != nil {
			log.Printf("polymarket parse offset=%d: %v", offset, err)
			continue
		}
		kept := 0
		for _, ev := range events {
			for _, m := range ev.Markets {
				if !isFinancialMarket(m.Question) {
					continue
				}
				if seen[m.ConditionID] {
					continue
				}
				seen[m.ConditionID] = true
				// Propagate event-level fields to the market
				m.EndDate = firstNonEmpty(m.EndDate, ev.EndDate)
				m.Events = []struct {
					Title    string `json:"title"`
					Category string `json:"category"`
				}{{Title: ev.Title}}
				raw = append(raw, m)
				kept++
			}
		}
		log.Printf("polymarket offset=%d: kept %d financial/crypto markets", offset, kept)
	}

	now := time.Now().UTC()
	var markets []models.NormalizedMarket
	for _, r := range raw {
		m, err := p.normalizeMarket(r, now)
		if err != nil {
			log.Printf("polymarket normalize skip %s: %v", r.ConditionID, err)
			continue
		}
		markets = append(markets, m)
	}
	log.Printf("polymarket total: %d financial/crypto markets normalized", len(markets))
	return markets, nil
}

// gammaEvent is the shape of the Polymarket Gamma /events response element.
type gammaEvent struct {
	Title   string        `json:"title"`
	EndDate string        `json:"endDate"`
	Markets []gammaMarket `json:"markets"`
}

func isFinancialMarket(question string) bool {
	q := strings.ToLower(question)
	for _, kw := range polymarketFinKeywords {
		if strings.Contains(q, kw) {
			return true
		}
	}
	return false
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (p *PolymarketConnector) normalizeMarket(r gammaMarket, fetchedAt time.Time) (models.NormalizedMarket, error) {
	// Parse outcomePrices JSON string → []float64
	var priceStrs []string
	if err := json.Unmarshal([]byte(r.OutcomePrices), &priceStrs); err != nil {
		return models.NormalizedMarket{}, fmt.Errorf("outcomePrices parse: %w", err)
	}
	if len(priceStrs) < 2 {
		return models.NormalizedMarket{}, fmt.Errorf("outcomePrices too short")
	}
	yesPrice, err := strconv.ParseFloat(strings.TrimSpace(priceStrs[0]), 64)
	if err != nil {
		return models.NormalizedMarket{}, fmt.Errorf("yesPrice parse: %w", err)
	}
	noPrice, err := strconv.ParseFloat(strings.TrimSpace(priceStrs[1]), 64)
	if err != nil {
		return models.NormalizedMarket{}, fmt.Errorf("noPrice parse: %w", err)
	}

	// Parse end date
	var resTime time.Time
	if r.EndDate != "" {
		resTime, _ = time.Parse(time.RFC3339, r.EndDate)
		if resTime.IsZero() {
			resTime, _ = time.Parse("2006-01-02T15:04:05Z", r.EndDate)
		}
		if resTime.IsZero() {
			resTime, _ = time.Parse("2006-01-02", r.EndDate)
		}
	}

	title := r.Question
	if title == "" {
		title = r.Slug
	}

	cat := inferPolymarketCategory(r.Events)

	// Extract YES outcome token ID from clobTokenIds (JSON-encoded string array)
	var tokenID string
	if r.ClobTokenIds != "" {
		var tokenIDs []string
		if err := json.Unmarshal([]byte(r.ClobTokenIds), &tokenIDs); err == nil && len(tokenIDs) > 0 {
			tokenID = tokenIDs[0]
		}
	}

	m := models.NormalizedMarket{
		VenueID:        "POLYMARKET",
		InternalID:     r.ConditionID,
		TokenID:        tokenID,
		Title:          title,
		TitleNorm:      matching.NormalizeTitle(title),
		YesPrice:       yesPrice,
		NoPrice:        noPrice,
		Category:       cat,
		ResolutionTime: resTime.UTC(),
		FetchedAt:      fetchedAt,
	}
	return m, nil
}

// clobOrderbookResp is the raw shape from /orderbook/{token_id}
type clobOrderbookResp struct {
	Asks []struct {
		Price string `json:"price"`
		Size  string `json:"size"`
	} `json:"asks"`
}

// FetchOrderbook populates Asks for a market using the CLOB API.
func (p *PolymarketConnector) FetchOrderbook(market *models.NormalizedMarket) error {
	if market.TokenID == "" {
		log.Printf("polymarket clob orderbook %s: no token_id available, skipping", market.InternalID)
		return nil
	}

	url := fmt.Sprintf("%s/book?token_id=%s", polymarketCLOBBase, market.TokenID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		log.Printf("polymarket clob orderbook %s: %v", market.TokenID, err)
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var ob clobOrderbookResp
	if err := json.Unmarshal(body, &ob); err != nil {
		log.Printf("polymarket clob orderbook %s: parse error: %v", market.TokenID, err)
		return nil
	}

	var asks []models.OrderbookLevel
	for _, a := range ob.Asks {
		price, err1 := strconv.ParseFloat(a.Price, 64)
		size, err2 := strconv.ParseFloat(a.Size, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		asks = append(asks, models.OrderbookLevel{Price: price, SizeUSD: size * price})
	}

	sort.Slice(asks, func(i, j int) bool { return asks[i].Price < asks[j].Price })

	var total float64
	for _, a := range asks {
		total += a.SizeUSD
	}

	market.Asks = asks
	market.TotalDepthUSD = total
	return nil
}

func inferPolymarketCategory(events []struct {
	Title    string `json:"title"`
	Category string `json:"category"`
}) string {
	for _, e := range events {
		cat := strings.ToLower(e.Category)
		switch cat {
		case "crypto", "bitcoin", "ethereum", "defi":
			return "crypto"
		case "politics", "elections", "government":
			return "politics"
		case "economics", "economy", "fed", "inflation", "finance":
			return "economics"
		case "sports", "nfl", "nba", "mlb", "soccer":
			return "sports"
		}
		// Fallback: infer from event title
		title := strings.ToLower(e.Title)
		switch {
		case strings.Contains(title, "bitcoin") || strings.Contains(title, "crypto") || strings.Contains(title, "eth"):
			return "crypto"
		case strings.Contains(title, "election") || strings.Contains(title, "president") || strings.Contains(title, "senate"):
			return "politics"
		case strings.Contains(title, "fed") || strings.Contains(title, "inflation") || strings.Contains(title, "gdp"):
			return "economics"
		case strings.Contains(title, "nba") || strings.Contains(title, "nfl") || strings.Contains(title, "mlb") || strings.Contains(title, "soccer"):
			return "sports"
		}
	}
	return ""
}
