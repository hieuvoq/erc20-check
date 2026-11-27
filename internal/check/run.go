package check

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

type Config struct {
	RPC, Token, Pool, Quote, FixtureRouter, Amount, Bearer string
	Chain, Block                                           uint64
	WarningBPS                                             int64
	SkipProvider                                           bool
	Prior                                                  *Report
}

func Run(ctx context.Context, c Config) (Report, error) {
	c.Token = strings.ToLower(c.Token)
	c.Pool = strings.ToLower(c.Pool)
	c.Quote = strings.ToLower(c.Quote)
	if !ValidURL(c.RPC) || !addressRE.MatchString(c.Token) || c.Token == zero {
		return Report{}, errors.New("valid HTTP RPC and nonzero token address required")
	}
	for _, a := range []string{c.Pool, c.Quote} {
		if a != "" && (!addressRE.MatchString(a) || a == zero) {
			return Report{}, errors.New("invalid pool or quote address")
		}
	}
	router := mainRouter
	if c.Chain == 31337 && addressRE.MatchString(c.FixtureRouter) {
		router = strings.ToLower(c.FixtureRouter)
	} else if c.Chain != 1 || c.FixtureRouter != "" {
		return Report{}, errors.New("supported networks: Ethereum mainnet, or chain 31337 with -fixture-router")
	}
	amount, ok := new(big.Int).SetString(c.Amount, 10)
	if !ok || amount.Sign() <= 0 || amount.BitLen() > 256 {
		return Report{}, errors.New("input wei must be a positive uint256")
	}
	if c.WarningBPS < 1 || c.WarningBPS > 10000 {
		return Report{}, errors.New("warning-bps must be 1..10000")
	}
	r := Report{Token: c.Token, Pool: c.Pool, Chain: c.Chain, Router: router, Quote: c.Quote, Amount: amount.String(), At: now(), Threshold: c.WarningBPS, Legs: map[string]Leg{}, Provider: Provider{Status: "skipped"}}
	for _, k := range required {
		r.Legs[k] = Leg{Status: "not_run"}
	}
	if err := simulate(ctx, c, &r, amount); err != nil {
		r.ProbeError = err.Error()
		r.Reasons = append(r.Reasons, r.ProbeError)
	}
	if !c.SkipProvider && c.Chain == 1 {
		r.Provider = fetchProvider(ctx, "https://api.gopluslabs.io/api/v1/token_security", c.Chain, c.Token, c.Bearer)
	}
	compare(&r)
	classify(&r, c.Prior)
	return r, nil
}

func simulate(ctx context.Context, c Config, r *Report, amount *big.Int) error {
	upstream := rpc{url: c.RPC}
	id, err := scalar(ctx, upstream, "eth_chainId")
	if err != nil {
		return err
	}
	chain, err := number(id)
	if err != nil || !chain.IsUint64() || chain.Uint64() != c.Chain {
		return errors.New("upstream chain ID mismatch")
	}
	tag := "finalized"
	if c.Block > 0 {
		tag = fmt.Sprintf("0x%x", c.Block)
	}
	b, err := block(ctx, upstream, tag)
	if err != nil {
		return err
	}
	h, err := number(b.Number)
	if err != nil || !h.IsUint64() {
		return errors.New("invalid block height")
	}
	r.Block = h.Uint64()
	r.Hash = b.Hash
	local, close, err := fork(ctx, c.RPC, b.Number)
	if err != nil {
		return err
	}
	defer close()
	pinned, err := block(ctx, local, b.Number)
	if err != nil {
		return err
	}
	if pinned.Hash != b.Hash {
		return errors.New("fork hash differs from upstream checkpoint")
	}
	var accounts []string
	if err = local.call(ctx, "eth_accounts", []any{}, &accounts); err != nil {
		return err
	}
	if len(accounts) < 2 || !addressRE.MatchString(accounts[0]) || !addressRE.MatchString(accounts[1]) {
		return errors.New("fork test accounts unavailable")
	}
	r.Wallet = accounts[0]
	r.Recipient = accounts[1]
	n := node{local, ctx}
	m, err := n.market(r.Router, c.Token, c.Quote, r.Wallet, r.Recipient)
	if err != nil {
		return err
	}
	if c.Pool != "" && c.Pool != m.pool {
		return errors.New("requested pool differs from factory pair")
	}
	r.Pool = m.pool
	r.Path = m.path
	control := ""
	if c.Chain == 1 {
		control = mainUSDC
		if c.Token == mainUSDC {
			control = mainDAI
		}
	}
	return m.probe(r, amount, control)
}
