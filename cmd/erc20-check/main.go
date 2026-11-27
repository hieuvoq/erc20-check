package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hieuvoq/erc20-check/internal/check"
)

func main() { os.Exit(run()) }
func run() int {
	var c check.Config
	flag.StringVar(&c.Token, "token", "", "target ERC-20 address")
	flag.StringVar(&c.RPC, "rpc", os.Getenv("ETH_RPC_URL"), "read-only upstream RPC (or ETH_RPC_URL)")
	flag.StringVar(&c.Quote, "quote", "", "target pool counter-asset (default WETH)")
	flag.StringVar(&c.Pool, "pool", "", "require this factory pair address")
	flag.Uint64Var(&c.Chain, "chain", 1, "chain ID")
	flag.Uint64Var(&c.Block, "block", 0, "fork height (default finalized)")
	flag.StringVar(&c.Amount, "amount-wei", "10000000000000000", "native asset to wrap and spend on the fork")
	flag.Int64Var(&c.WarningBPS, "warning-bps", 1000, "inclusive tax warning threshold")
	flag.StringVar(&c.FixtureRouter, "fixture-router", "", "local chain 31337 Router02")
	flag.BoolVar(&c.SkipProvider, "no-goplus", false, "skip external provider comparison")
	prior := flag.String("previous", "", "prior JSON report for the same market")
	flag.Parse()
	if flag.NArg() != 0 {
		return 2
	}
	c.Bearer = os.Getenv("GOPLUS_TOKEN")
	if *prior != "" {
		f, err := os.Open(*prior)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		defer f.Close()
		c.Prior = new(check.Report)
		if err = json.NewDecoder(f).Decode(c.Prior); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	report, err := check.Run(ctx, c)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err = enc.Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if report.Mark == "UNKNOWN" {
		return 3
	}
	return 0
}
