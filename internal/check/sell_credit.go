package check

import (
	"errors"
	"math/big"
	"strings"
)

const transferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
const syncTopic = "0x1c411e9a96e071241c2f21f7726b17ae89e3cab4c78be50e062b03a9fffbbad1"

type pairSwap struct {
	offset  int
	amounts []*big.Int
}

func (l logEntry) isEvent(emitter, topic string) bool {
	return strings.EqualFold(l.Address, emitter) && len(l.Topics) > 0 && strings.EqualFold(l.Topics[0], topic)
}

func pairSwaps(rec receipt, pool string) ([]pairSwap, error) {
	var swaps []pairSwap
	for i, l := range rec.Logs {
		if !l.isEvent(pool, swapTopic) {
			continue
		}
		v, err := words(l.Data, 4)
		if err != nil {
			return nil, err
		}
		swaps = append(swaps, pairSwap{i, v})
	}
	if len(swaps) == 0 {
		return nil, errors.New("missing pair Swap event")
	}
	return swaps, nil
}

func (m market) sellCredit(rec receipt, swaps []pairSwap, delta *big.Int) (*big.Int, *SellAttribution, error) {
	if len(swaps) == 1 {
		if swaps[0].amounts[m.side].Cmp(delta) != 0 {
			return nil, nil, errors.New("pair swap input differs from balance delta")
		}
		return delta, nil, nil
	}
	// The Swap sender is the router on both the user's trade and some nested trades.
	// Select by sender, route destination and direction, then verify the funding below.
	routerTopic, toTopic := "0x"+addr(m.router), "0x"+addr(m.sellTo)
	var seller *pairSwap
	for i, s := range swaps {
		t := rec.Logs[s.offset].Topics
		if len(t) != 3 {
			return nil, nil, errors.New("invalid indexed topics on pair Swap")
		}
		v := s.amounts
		onRoute := strings.EqualFold(t[1], routerTopic) && strings.EqualFold(t[2], toTopic)
		targetIn := v[m.side].Sign() > 0 && v[1-m.side].Sign() == 0
		quoteOut := v[2+m.side].Sign() == 0 && v[3-m.side].Sign() > 0
		if !onRoute || !targetIn || !quoteOut {
			continue
		}
		if seller != nil {
			return nil, nil, errors.New("multiple pair swaps match the sale route")
		}
		seller = &swaps[i]
	}
	if seller == nil {
		return nil, nil, errors.New("no pair swap matches the sale route")
	}

	// A V2 pair emits Sync just before Swap. Mint, Burn, sync() and other swaps
	// also reset reserves, so only transfers since the preceding Sync fund this leg.
	end := seller.offset - 1
	if end < 0 || !rec.Logs[end].isEvent(m.pool, syncTopic) {
		return nil, nil, errors.New("missing Sync before seller swap")
	}
	start := end
	for start > 0 && !rec.Logs[start-1].isEvent(m.pool, syncTopic) {
		start--
	}
	walletTopic, poolTopic := "0x"+addr(m.wallet), "0x"+addr(m.pool)
	credit, net := new(big.Int), new(big.Int)
	for i, l := range rec.Logs {
		if !l.isEvent(m.token, transferTopic) {
			continue
		}
		if len(l.Topics) != 3 {
			return nil, nil, errors.New("invalid indexed topics on token Transfer")
		}
		fromPool, toPool := strings.EqualFold(l.Topics[1], poolTopic), strings.EqualFold(l.Topics[2], poolTopic)
		if !fromPool && !toPool {
			continue
		}
		v, err := words(l.Data, 1)
		if err != nil {
			return nil, nil, err
		}
		if v[0].Sign() == 0 {
			continue
		}
		if fromPool {
			net.Sub(net, v[0])
		}
		if toPool {
			net.Add(net, v[0])
		}
		fromSeller := toPool && strings.EqualFold(l.Topics[1], walletTopic)
		inWindow := i >= start && i < end
		// Only the seller may fund this interval, and its credits must all fall inside it.
		if fromSeller != inWindow {
			return nil, nil, errors.New("cannot isolate wallet funding within the seller swap reserve interval")
		}
		if fromSeller {
			credit.Add(credit, v[0])
		}
	}
	if net.Cmp(delta) != 0 || credit.Cmp(seller.amounts[m.side]) != 0 {
		return nil, nil, errors.New("token transfers do not reconcile with pool balances and seller swap input")
	}
	return credit, &SellAttribution{PairSwaps: len(swaps), SwapOffset: seller.offset, PoolDelta: delta.String()}, nil
}
