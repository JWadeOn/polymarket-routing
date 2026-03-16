// Command equinox is the CLI entry point for Project Equinox.
// It fetches live markets, matches cross-venue pairs, and prints routing decisions.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/jwadeon/equinox/internal/connector"
	"github.com/jwadeon/equinox/internal/simulation"
)

func main() {
	reportFlag := flag.Bool("report", false, "write a self-contained report.html after the simulation runs")
	flag.Parse()

	poly := connector.NewPolymarketConnector()
	kalshi := connector.NewKalshiConnector()

	if *reportFlag {
		decisions, matches, polyMarkets, kalshiMarkets, polyCount, kalshiCount, err := simulation.RunAndCollect(poly, kalshi)
		if err != nil {
			fmt.Fprintf(os.Stderr, "equinox: %v\n", err)
			os.Exit(1)
		}
		log.Printf("report: passing %d matches to GenerateReport()", len(matches))
		htmlBytes, err := simulation.GenerateReport(decisions, matches, polyMarkets, kalshiMarkets, polyCount, kalshiCount)
		if err != nil {
			fmt.Fprintf(os.Stderr, "equinox: report: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile("report.html", htmlBytes, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "equinox: write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Report written to report.html")
	} else {
		if err := simulation.Run(poly, kalshi); err != nil {
			fmt.Fprintf(os.Stderr, "equinox: %v\n", err)
			os.Exit(1)
		}
	}
}
