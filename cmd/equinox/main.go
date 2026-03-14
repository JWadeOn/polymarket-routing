// Command equinox is the CLI entry point for Project Equinox.
// It fetches live markets, matches cross-venue pairs, and prints routing decisions.
package main

import (
	"fmt"
	"os"

	"github.com/jwadeon/equinox/internal/connector"
	"github.com/jwadeon/equinox/internal/simulation"
)

func main() {
	poly := connector.NewPolymarketConnector()
	kalshi := connector.NewKalshiConnector()

	if err := simulation.Run(poly, kalshi); err != nil {
		fmt.Fprintf(os.Stderr, "equinox: %v\n", err)
		os.Exit(1)
	}
}
