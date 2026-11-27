package check

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
)

type Provider struct {
	Status        string            `json:"status"`
	At            string            `json:"queried_at,omitempty"`
	Reason        string            `json:"reason,omitempty"`
	Fields        map[string]string `json:"fields,omitempty"`
	Disagreements []string          `json:"disagreements,omitempty"`
}

func fetchProvider(ctx context.Context, base string, chain uint64, token, bearer string) Provider {
	p := Provider{Status: "unavailable", At: now()}
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/%d?contract_addresses=%s", base, chain, token), nil)
	if err != nil {
		p.Reason = "invalid provider URL"
		return p
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := httpClient.Do(req)
	if err != nil {
		p.Reason = "request failed"
		return p
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		p.Reason = res.Status
		return p
	}
	var reply struct {
		Code   int
		Result map[string]map[string]json.RawMessage
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&reply)
	if err != nil || reply.Code != 1 || reply.Result[token] == nil {
		p.Reason = "no successful result for this address"
		return p
	}
	p.Fields = map[string]string{}
	p.Status = "available"
	for _, key := range []string{"buy_tax", "sell_tax", "transfer_tax", "cannot_buy", "cannot_sell_all", "is_honeypot", "slippage_modifiable", "transfer_pausable", "trading_cooldown"} {
		var value string
		if raw := reply.Result[token][key]; len(raw) > 0 && json.Unmarshal(raw, &value) == nil && value != "" {
			p.Fields[key] = value
		}
	}
	return p
}
func compare(r *Report) {
	p := &r.Provider
	if p.Status != "available" {
		return
	}
	for _, item := range []struct{ field, leg string }{{"buy_tax", "buy"}, {"sell_tax", "sell_partial"}, {"sell_tax", "sell_all"}, {"transfer_tax", "transfer"}} {
		l := r.Legs[item.leg]
		raw := p.Fields[item.field]
		if raw == "" || l.TaxBPS == nil {
			continue
		}
		fraction, ok := new(big.Rat).SetString(raw)
		if !ok || fraction.Sign() < 0 || fraction.Cmp(big.NewRat(1, 1)) > 0 {
			continue
		}
		providerBPS := fraction.Mul(fraction, big.NewRat(10000, 1))
		diff := new(big.Rat).Sub(providerBPS, big.NewRat(*l.TaxBPS, 1))
		// Tolerate one basis point for integer rounding and provider precision.
		if diff.Abs(diff).Cmp(big.NewRat(1, 1)) > 0 {
			p.Disagreements = append(p.Disagreements, item.leg+": local "+strconv.FormatInt(*l.TaxBPS, 10)+" bps; GoPlus fraction "+raw)
		}
	}
	for _, item := range []struct{ field, leg string }{{"cannot_buy", "buy"}, {"cannot_sell_all", "sell_all"}, {"is_honeypot", "sell_all"}} {
		value := p.Fields[item.field]
		status := r.Legs[item.leg].Status
		if (value == "1" && status == "ok") || (value == "0" && status == "reverted") {
			p.Disagreements = append(p.Disagreements, fmt.Sprintf("GoPlus %s=%s; local %s=%s", item.field, value, item.leg, status))
		}
	}
}
