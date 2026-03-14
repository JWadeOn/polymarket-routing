// Package connector defines the VenueConnector interface and its implementations.
// All venue-specific normalization logic is confined to this package.
package connector

import "github.com/jwadeon/equinox/internal/models"

// VenueConnector is the interface all venue adapters must satisfy.
// The matching and routing layers interact only with this interface.
type VenueConnector interface {
	// FetchMarkets returns normalized markets for the given category filter.
	// Returns partial results + error if some markets fail to parse.
	FetchMarkets(category string) ([]models.NormalizedMarket, error)

	// FetchOrderbook populates the Asks field for a specific market.
	FetchOrderbook(market *models.NormalizedMarket) error

	// VenueID returns the canonical venue identifier ("KALSHI" | "POLYMARKET").
	VenueID() string
}
