package check

import (
	"math/big"
	"testing"
)

func TestMultipleSwapSellCredit(t *testing.T) {
	const pool = "0x0000000000000000000000000000000000000001"
	const wallet = "0x0000000000000000000000000000000000000002"
	const collector = "0x0000000000000000000000000000000000000003"
	const nextPair = "0x0000000000000000000000000000000000000004"
	m := market{pool: pool, token: mainUSDC, wallet: wallet, router: mainRouter, sellTo: nextPair}
	transfer := func(from, to string, v int64) logEntry {
		return logEntry{Address: m.token, Topics: []string{transferTopic, "0x" + addr(from), "0x" + addr(to)}, Data: "0x" + uintArg(v)}
	}
	swap := func(to string, amount int64) logEntry {
		return logEntry{Address: pool, Topics: []string{swapTopic, "0x" + addr(m.router), "0x" + addr(to)}, Data: "0x" + uintArg(amount) + uintArg(0) + uintArg(0) + uintArg(5)}
	}
	sync := logEntry{Address: pool, Topics: []string{syncTopic}}
	// A fee conversion and a liquidity addition fund the same pool before the seller does.
	fixture := []logEntry{transfer(collector, pool, 40), sync, swap(mainRouter, 40), transfer(collector, pool, 7), sync,
		transfer(wallet, collector, 3), transfer(wallet, pool, 97), sync, swap(nextPair, 97)}
	run := func(logs []logEntry, delta int64) (*big.Int, *SellAttribution, error) {
		rec := receipt{Logs: logs}
		swaps, err := pairSwaps(rec, pool)
		if err != nil {
			return nil, nil, err
		}
		return m.sellCredit(rec, swaps, big.NewInt(delta))
	}
	check := func(logs []logEntry, delta int64, wantOffset int) {
		t.Helper()
		credit, a, err := run(logs, delta)
		if err != nil || credit.Int64() != 97 || a.PairSwaps != 2 || a.SwapOffset != wantOffset || a.PoolDelta != big.NewInt(delta).String() {
			t.Fatalf("credit=%v evidence=%+v err=%v", credit, a, err)
		}
	}
	check(fixture, 144, 8)
	// The wallet's route can also complete before an unrelated swap.
	logs := append([]logEntry{}, fixture[5:]...)
	logs = append(logs, fixture[:5]...)
	check(logs, 144, 3)
	for _, tc := range []struct {
		name        string
		pos         int
		replacement logEntry
		delta       int64
	}{
		{"duplicate route", 2, swap(nextPair, 40), 144},
		{"wrong origin", 6, transfer(collector, pool, 97), 144},
		{"altered swap amount", 8, swap(nextPair, 98), 144},
		{"missing sync", 7, logEntry{}, 144},
		{"foreign token", 6, logEntry{Address: mainDAI, Topics: fixture[6].Topics, Data: fixture[6].Data}, 144},
		{"pool delta mismatch", 8, fixture[8], 145},
		{"wallet also funded earlier swap", 0, transfer(wallet, pool, 40), 144},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := append([]logEntry{}, fixture...)
			logs[tc.pos] = tc.replacement
			if _, _, err := run(logs, tc.delta); err == nil {
				t.Fatal("accepted ambiguous or inconsistent evidence")
			}
		})
	}
}
