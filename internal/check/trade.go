package check

import (
	"errors"
	"fmt"
	"math/big"
)

type market struct {
	node
	router, pool, token, wallet, recipient, sellTo string
	path                                           []string
	side                                           int
}

func (n node) market(router, token, quote, wallet, recipient string) (market, error) {
	m := market{node: n, router: router, token: token, wallet: wallet, recipient: recipient}
	weth, err := n.address(router, "0xad5c4648")
	if err != nil {
		return m, err
	}
	factory, err := n.address(router, "0xc45a0155")
	if err != nil {
		return m, err
	}
	if quote == "" {
		quote = weth
	}
	if token == quote || token == weth {
		return m, errors.New("target must differ from wrapped native and quote asset")
	}
	m.path = []string{weth}
	m.sellTo = wallet
	if quote != weth {
		// On a reverse two-hop route, Router02 sends the first output to the next pair.
		m.sellTo, err = n.address(factory, "0xe6a43905"+addr(quote)+addr(weth))
		if err != nil {
			return m, err
		}
		if m.sellTo == zero {
			return m, errors.New("factory has no quote/WETH pair")
		}
		m.path = append(m.path, quote)
	}
	m.path = append(m.path, token)
	m.pool, err = n.address(factory, "0xe6a43905"+addr(token)+addr(quote))
	if err != nil {
		return m, err
	}
	if m.pool == zero {
		return m, errors.New("factory has no pair for target and quote")
	}
	t0, err := n.address(m.pool, "0x0dfe1681")
	if err != nil {
		return m, err
	}
	t1, err := n.address(m.pool, "0xd21220a7")
	if err != nil {
		return m, err
	}
	switch {
	case t0 == token && t1 == quote:
		m.side = 0
	case t1 == token && t0 == quote:
		m.side = 1
	default:
		return m, errors.New("pair currencies do not match")
	}
	return m, nil
}
func (m market) approve(token string, a *big.Int) Leg {
	h, _, err := m.send(m.wallet, token, "0x095ea7b3"+addr(m.router)+word(a), nil)
	if err != nil {
		return failed(h, err)
	}
	got, err := m.uint(token, "0xdd62ed3e"+addr(m.wallet)+addr(m.router))
	if err != nil {
		return failed(h, err)
	}
	if got.Cmp(a) < 0 {
		return failed(h, errors.New("approval receipt succeeded but allowance was insufficient"))
	}
	return Leg{Status: "ok", Tx: h, Sent: a.String()}
}

// Check a no-tax reference quote only for viability. It is never the tax denominator.
func (m market) quote(amount *big.Int, path []string) (*big.Int, error) {
	data := "0xd06ca61f" + word(amount) + uintArg(64) + uintArg(int64(len(path)))
	for _, a := range path {
		data += addr(a)
	}
	raw, err := m.read(m.router, data)
	if err != nil {
		return nil, fmt.Errorf("reference quote unavailable: %w", err)
	}
	vals, err := words(raw, len(path)+2)
	if err != nil {
		return nil, err
	}
	if vals[0].Cmp(big.NewInt(32)) != 0 || vals[1].Cmp(big.NewInt(int64(len(path)))) != 0 || vals[len(vals)-1].Sign() == 0 {
		return nil, errors.New("reference quote empty or rounded to zero")
	}
	return vals[len(vals)-1], nil
}
func (m market) trade(buy bool, amount *big.Int) (result Leg) {
	path := m.path
	if !buy {
		path = reverse(path)
	}
	quoted, err := m.quote(amount, path)
	defer func() {
		result.Input = amount.String()
		if quoted != nil {
			result.QuoteOut = quoted.String()
		}
	}()
	if err != nil {
		return failed("", err)
	}
	wallet, err := m.balance(m.token, m.wallet)
	if err != nil {
		return failed("", err)
	}
	pair, err := m.balance(m.token, m.pool)
	if err != nil {
		return failed("", err)
	}
	var proceedsBefore *big.Int
	if !buy {
		proceedsBefore, err = m.balance(path[len(path)-1], m.wallet)
		if err != nil {
			return failed("", err)
		}
	}
	h, rec, err := m.send(m.wallet, m.router, swapData(amount, path, m.wallet), nil)
	if err != nil {
		return failed(h, err)
	}
	after, err := m.balance(m.token, m.wallet)
	if err != nil {
		return failed(h, err)
	}
	pairAfter, err := m.balance(m.token, m.pool)
	if err != nil {
		return failed(h, err)
	}
	swaps, err := pairSwaps(rec, m.pool)
	if err != nil {
		return failed(h, err)
	}
	if buy {
		if len(swaps) != 1 {
			return failed(h, errors.New("ambiguous pair Swap events on buy"))
		}
		out := new(big.Int).Sub(after, wallet)
		result = measure(swaps[0].amounts[m.side+2], new(big.Int).Sub(pair, pairAfter), out, h)
		result.ActualOut = out.String()
		return result
	}
	poolDelta := new(big.Int).Sub(pairAfter, pair)
	credit, attribution, err := m.sellCredit(rec, swaps, poolDelta)
	if err != nil {
		return failed(h, err)
	}
	proceeds, err := m.balance(path[len(path)-1], m.wallet)
	if err != nil {
		return failed(h, err)
	}
	out := new(big.Int).Sub(proceeds, proceedsBefore)
	if out.Sign() <= 0 {
		return failed(h, errors.New("sell receipt succeeded but wallet received no wrapped native proceeds"))
	}
	result = measure(amount, new(big.Int).Sub(wallet, after), credit, h)
	result.Attribution = attribution
	result.ActualOut = out.String()
	return result
}
func (m market) transfer(to string, amount *big.Int) Leg {
	fromBefore, err := m.balance(m.token, m.wallet)
	if err != nil {
		return failed("", err)
	}
	toBefore, err := m.balance(m.token, to)
	if err != nil {
		return failed("", err)
	}
	h, _, err := m.send(m.wallet, m.token, "0xa9059cbb"+addr(to)+word(amount), nil)
	if err != nil {
		return failed(h, err)
	}
	fromAfter, err := m.balance(m.token, m.wallet)
	if err != nil {
		return failed(h, err)
	}
	toAfter, err := m.balance(m.token, to)
	if err != nil {
		return failed(h, err)
	}
	return measure(amount, new(big.Int).Sub(fromBefore, fromAfter), new(big.Int).Sub(toAfter, toBefore), h)
}
func (m market) buy(amount *big.Int) Leg {
	_, _, err := m.send(m.wallet, m.path[0], "0xd0e30db0", amount)
	if err != nil {
		return failed("", fmt.Errorf("wrap native asset: %v", err))
	}
	if a := m.approve(m.path[0], amount); a.Status != "ok" {
		return failed(a.Tx, fmt.Errorf("wrapped native approval: %s", a.Reason))
	}
	return m.trade(true, amount)
}
func (m market) probe(r *Report, amount *big.Int, controlToken string) error {
	// Use newly generated accounts. A pre-existing token holding would invalidate the full-sale test.
	initial, err := m.balance(m.token, m.wallet)
	if err != nil {
		return err
	}
	if initial.Sign() != 0 {
		return errors.New("test holder already owns target")
	}
	buy := m.buy(amount)
	r.Legs["buy"] = buy
	if buy.Status != "ok" {
		return nil
	}
	held, err := m.balance(m.token, m.wallet)
	if err != nil {
		return err
	}
	if held.Cmp(big.NewInt(2)) < 0 {
		return errors.New("purchase yielded fewer than two raw units")
	}
	half := new(big.Int).Quo(held, big.NewInt(2))
	// Each branch starts from the same post-buy state. Approval is shared only by sells.
	if err = m.isolated(func() error { r.Legs["transfer"] = m.transfer(m.recipient, half); return nil }); err != nil {
		return err
	}
	r.Legs["approve"] = m.approve(m.token, held)
	if r.Legs["approve"].Status != "ok" {
		return nil
	}
	for _, test := range []struct {
		name   string
		amount *big.Int
	}{{"sell_partial", half}, {"sell_all", held}} {
		if err = m.isolated(func() error { r.Legs[test.name] = m.trade(false, test.amount); return nil }); err != nil {
			return err
		}
	}
	if r.Legs["sell_partial"].Status == "reverted" && r.Legs["sell_all"].Status == "reverted" {
		if err = m.isolated(func() error { r.Legs["pool_transfer"] = m.transfer(m.pool, half); return nil }); err != nil {
			return err
		}
		if controlToken != "" {
			ref, err := m.node.market(m.router, controlToken, "", m.wallet, m.recipient)
			if err != nil {
				r.Legs["control_roundtrip"] = failed("", err)
				return nil
			}
			r.ControlPool = ref.pool
			r.ControlToken = controlToken
			b := ref.buy(amount)
			r.Legs["control_buy"] = b
			if b.Status == "ok" {
				value, _ := new(big.Int).SetString(b.Received, 10)
				a := ref.approve(controlToken, value)
				if a.Status == "ok" {
					r.Legs["control_roundtrip"] = ref.trade(false, value)
				} else {
					r.Legs["control_roundtrip"] = a
				}
			}
		}
	}
	return nil
}
