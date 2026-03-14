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

const kalshiBase = "https://api.elections.kalshi.com/trade-api/v2"

// KalshiConnector implements VenueConnector for the Kalshi REST v2 API.
type KalshiConnector struct {
	client *http.Client
}

// NewKalshiConnector returns a KalshiConnector with a default HTTP client.
func NewKalshiConnector() *KalshiConnector {
	return &KalshiConnector{
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (k *KalshiConnector) VenueID() string { return "KALSHI" }

// kalshiMarket is the raw shape returned by the Kalshi /markets endpoint.
type kalshiMarket struct {
	Ticker        string `json:"ticker"`
	Title         string `json:"title"`
	Subtitle      string `json:"subtitle"`
	YesSubTitle   string `json:"yes_sub_title"`
	YesAskDollars string `json:"yes_ask_dollars"` // decimal string e.g. "0.6500"
	NoAskDollars  string `json:"no_ask_dollars"`  // decimal string
	CloseTime     string `json:"close_time"`
	EventTicker   string `json:"event_ticker"`
	Status        string `json:"status"`
	MarketType    string `json:"market_type"`
}

// kalshiMarketsResponse wraps the /markets response envelope.
type kalshiMarketsResponse struct {
	Markets []kalshiMarket `json:"markets"`
	Cursor  string         `json:"cursor"`
}

// kalshiTargetSeries are the series_ticker values covering financial and crypto markets.
// The Kalshi /markets category filter is non-functional on the public API;
// series_ticker is the reliable parameter for targeted fetches.
var kalshiTargetSeries = []string{
	"KXBTC",     // Bitcoin price range
	"KXETH",     // Ethereum price
	"KXCPI",     // CPI month-over-month
	"KXCPIYOY",  // CPI year-over-year
	"KXFED",     // Federal funds rate upper bound
	"KXRATECUT", // Fed rate cut binary
	"KXGDP",     // Real GDP growth
}

// FetchMarkets fetches active financial and crypto markets from Kalshi REST v2.
// Makes one request per series_ticker and deduplicates by ticker before normalizing.
// (The Kalshi public API category= parameter is non-functional; series_ticker is used instead.)
func (k *KalshiConnector) FetchMarkets(category string) ([]models.NormalizedMarket, error) {
	seen := map[string]bool{}
	var raw []kalshiMarket

	for _, series := range kalshiTargetSeries {
		url := fmt.Sprintf("%s/markets?status=open&limit=100&series_ticker=%s", kalshiBase, series)
		resp, err := k.client.Get(url)
		if err != nil {
			log.Printf("kalshi fetch series=%s: %v", series, err)
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Printf("kalshi read series=%s: %v", series, err)
			continue
		}
		var envelope kalshiMarketsResponse
		if err := json.Unmarshal(body, &envelope); err != nil {
			log.Printf("kalshi parse series=%s: %v", series, err)
			continue
		}
		log.Printf("kalshi series=%s: fetched %d markets", series, len(envelope.Markets))
		for _, m := range envelope.Markets {
			if !seen[m.Ticker] {
				seen[m.Ticker] = true
				raw = append(raw, m)
			}
		}
	}

	now := time.Now().UTC()
	var markets []models.NormalizedMarket
	for _, r := range raw {
		m, err := k.normalizeMarket(r, now)
		if err != nil {
			log.Printf("kalshi normalize skip %s: %v", r.Ticker, err)
			continue
		}
		markets = append(markets, m)
	}
	return markets, nil
}

func (k *KalshiConnector) normalizeMarket(r kalshiMarket, fetchedAt time.Time) (models.NormalizedMarket, error) {
	// yes_ask_dollars is already in decimal (not cents) — no division needed.
	var yesPrice float64
	if r.YesAskDollars != "" {
		v, err := strconv.ParseFloat(strings.TrimSpace(r.YesAskDollars), 64)
		if err != nil {
			return models.NormalizedMarket{}, fmt.Errorf("yes_ask_dollars parse %q: %w", r.YesAskDollars, err)
		}
		yesPrice = v
	} else {
		return models.NormalizedMarket{}, fmt.Errorf("no yes_ask_dollars field for %s", r.Ticker)
	}

	// Reject markets with no meaningful price data
	if yesPrice <= 0 || yesPrice >= 1 {
		return models.NormalizedMarket{}, fmt.Errorf("degenerate price %.4f for %s", yesPrice, r.Ticker)
	}

	var noPrice float64
	if r.NoAskDollars != "" {
		v, err := strconv.ParseFloat(strings.TrimSpace(r.NoAskDollars), 64)
		if err == nil && v > 0 {
			noPrice = v
		}
	}
	if noPrice == 0 {
		noPrice = 1.0 - yesPrice
	}

	var resTime time.Time
	if r.CloseTime != "" {
		resTime, _ = time.Parse(time.RFC3339, r.CloseTime)
		if resTime.IsZero() {
			resTime, _ = time.Parse("2006-01-02T15:04:05Z", r.CloseTime)
		}
	}

	title := r.Title
	if r.Subtitle != "" {
		title = title + " " + r.Subtitle
	}

	cat := inferKalshiCategory(r.EventTicker)

	m := models.NormalizedMarket{
		VenueID:        "KALSHI",
		InternalID:     r.Ticker,
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

// kalshiOrderbookResp is the raw shape from /markets/{ticker}/orderbook.
// The API returns orderbook_fp with yes_dollars/no_dollars as string pairs
// [["price_decimal", "size_usd"], ...] where price is already a decimal
// probability and size is the notional USD at that level.
type kalshiOrderbookResp struct {
	OrderbookFP struct {
		YesDollars [][]string `json:"yes_dollars"`
		NoDollars  [][]string `json:"no_dollars"`
	} `json:"orderbook_fp"`
}

// FetchOrderbook populates Asks for a Kalshi market.
func (k *KalshiConnector) FetchOrderbook(market *models.NormalizedMarket) error {
	url := fmt.Sprintf("%s/markets/%s/orderbook", kalshiBase, market.InternalID)
	resp, err := k.client.Get(url)
	if err != nil {
		log.Printf("kalshi orderbook %s: %v", market.InternalID, err)
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var ob kalshiOrderbookResp
	if err := json.Unmarshal(body, &ob); err != nil {
		log.Printf("kalshi orderbook %s: parse error: %v", market.InternalID, err)
		return nil
	}

	// YES levels: [["price_decimal", "size_usd"]] — price already in [0,1], size in USD
	var asks []models.OrderbookLevel
	for _, level := range ob.OrderbookFP.YesDollars {
		if len(level) < 2 {
			continue
		}
		price, err1 := strconv.ParseFloat(level[0], 64)
		sizeUSD, err2 := strconv.ParseFloat(level[1], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		asks = append(asks, models.OrderbookLevel{Price: price, SizeUSD: sizeUSD})
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

func inferKalshiCategory(eventTicker string) string {
	t := strings.ToLower(eventTicker)
	switch {
	case strings.Contains(t, "crypto") || strings.Contains(t, "btc") || strings.Contains(t, "eth"):
		return "crypto"
	case strings.Contains(t, "election") || strings.Contains(t, "politics") || strings.Contains(t, "president"):
		return "politics"
	case strings.Contains(t, "econ") || strings.Contains(t, "fed") || strings.Contains(t, "cpi") || strings.Contains(t, "gdp") || strings.Contains(t, "rate"):
		return "economics"
	case strings.Contains(t, "sport") || strings.Contains(t, "nba") || strings.Contains(t, "nfl") || strings.Contains(t, "mlb"):
		return "sports"
	default:
		return ""
	}
}
