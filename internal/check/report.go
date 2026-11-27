package check

import (
	"errors"
	"fmt"
	"math/big"
)

type SellAttribution struct {
	PairSwaps  int    `json:"pair_swap_count"`
	SwapOffset int    `json:"seller_swap_receipt_offset"`
	PoolDelta  string `json:"pool_net_change_raw"`
}
type Leg struct {
	Attribution *SellAttribution `json:"multi_swap_attribution,omitempty"`
	Input       string           `json:"route_input_raw,omitempty"`
	QuoteOut    string           `json:"quoted_out_raw,omitempty"`
	ActualOut   string           `json:"actual_out_raw,omitempty"`
	Status      string           `json:"status"`
	Sent        string           `json:"nominal_raw,omitempty"`
	Received    string           `json:"received_raw,omitempty"`
	Debited     string           `json:"debited_raw,omitempty"`
	TaxBPS      *int64           `json:"tax_bps"`
	Tx          string           `json:"fork_tx,omitempty"`
	Reason      string           `json:"reason,omitempty"`
}
type Prior struct {
	Block uint64 `json:"block"`
	Hash  string `json:"hash"`
	Mark  string `json:"markLevel"`
}
type Report struct {
	ProbeError   string         `json:"probe_error,omitempty"`
	ControlPool  string         `json:"control_pool,omitempty"`
	ControlToken string         `json:"control_token,omitempty"`
	Chain        uint64         `json:"chain_id"`
	Token        string         `json:"token"`
	Pool         string         `json:"pool,omitempty"`
	Router       string         `json:"router"`
	Quote        string         `json:"quote_override,omitempty"`
	Path         []string       `json:"buy_path,omitempty"`
	Amount       string         `json:"input_wei"`
	Block        uint64         `json:"block"`
	Hash         string         `json:"block_hash,omitempty"`
	At           string         `json:"checked_at"`
	Wallet       string         `json:"fork_wallet,omitempty"`
	Recipient    string         `json:"fork_recipient,omitempty"`
	Legs         map[string]Leg `json:"legs"`
	Provider     Provider       `json:"goplus"`
	Previous     *Prior         `json:"previous,omitempty"`
	Threshold    int64          `json:"warning_bps"`
	Mark         string         `json:"markLevel"`
	Behavior     string         `json:"behavior"`
	Confidence   string         `json:"confidence"`
	Reasons      []string       `json:"reasons"`
}

var required = []string{"buy", "approve", "sell_partial", "sell_all", "transfer"}

func measure(sent, debit, received *big.Int, hash string) Leg {
	l := Leg{Status: "unknown", Tx: hash, Sent: sent.String(), Debited: debit.String(), Received: received.String()}
	if sent.Sign() <= 0 || sent.Cmp(debit) != 0 || received.Sign() < 0 || received.Cmp(sent) > 0 {
		l.Reason = "balance changes cannot isolate a recipient-side fee"
		return l
	}
	loss := new(big.Int).Sub(sent, received)
	bps := loss.Mul(loss, big.NewInt(10000)).Quo(loss, sent).Int64()
	l.Status = "ok"
	l.TaxBPS = &bps
	return l
}
func failed(hash string, err error) Leg {
	status := "unknown"
	var revert reverted
	if errors.As(err, &revert) {
		status = "reverted"
	}
	return Leg{Status: status, Tx: hash, Reason: err.Error()}
}
func restricted(r Report) bool {
	return r.ProbeError == "" && r.Legs["transfer"].Status == "ok" && r.Legs["buy"].Status == "ok" && r.Legs["approve"].Status == "ok" && r.Legs["sell_partial"].Status == "reverted" && r.Legs["sell_all"].Status == "reverted" && r.Legs["pool_transfer"].Status == "reverted" && r.Legs["control_roundtrip"].Status == "ok"
}
func classify(r *Report, prior *Report) {
	r.Mark = "UNKNOWN"
	r.Behavior = "incomplete"
	r.Confidence = "inconclusive"
	same := prior != nil && prior.Chain == r.Chain && prior.Token == r.Token && prior.Router == r.Router && prior.Amount == r.Amount && prior.Quote == r.Quote && prior.Pool != "" && (prior.Pool == r.Pool || r.Pool == "")
	if same {
		if prior.Mark == "UNKNOWN" && prior.Previous != nil {
			r.Previous = prior.Previous
		} else {
			r.Previous = &Prior{prior.Block, prior.Hash, prior.Mark}
		}
	}
	complete := r.ProbeError == ""
	warning := false
	taxed := false
	for _, k := range required {
		l := r.Legs[k]
		if l.Status != "ok" {
			complete = false
			r.Reasons = append(r.Reasons, k+": "+l.Status)
			if l.Status == "reverted" {
				warning = true
			}
			continue
		}
		if l.TaxBPS != nil && l.Sent != l.Received {
			taxed = true
			r.Reasons = append(r.Reasons, fmt.Sprintf("%s: recipient received %s of %s raw units", k, l.Received, l.Sent))
			if *l.TaxBPS >= r.Threshold {
				warning = true
			}
		}
	}
	switch {
	case restricted(*r):
		r.Mark = "WARNING"
		r.Behavior = "sell_restriction_observed"
		r.Confidence = "observed"
		if same && prior.Block > 0 && prior.Block < r.Block && hashRE.MatchString(prior.Hash) && prior.Hash != r.Hash && restricted(*prior) {
			r.Mark = "CRITICAL"
			r.Confidence = "repeated"
			r.Reasons = append(r.Reasons, "sell restriction on this pool in two distinct block observations; control round trips succeeded")
		}
	case r.Legs["sell_partial"].Status == "ok" && r.Legs["sell_all"].Status == "reverted":
		r.Mark = "WARNING"
		r.Behavior = "full_sale_reverted"
		r.Confidence = "observed"
	case warning:
		r.Mark = "WARNING"
		r.Behavior = "trade_anomaly"
		r.Confidence = "observed"
	case !complete:
		if taxed {
			r.Mark = "NOTICE"
			r.Behavior = "fee_observed_partial_coverage"
			r.Confidence = "observed"
		}
	case taxed:
		r.Mark = "NOTICE"
		r.Behavior = "fee_on_transfer"
		r.Confidence = "observed"
	default:
		r.Mark = "NORMAL"
		r.Behavior = "normal_in_tested_scenario"
		r.Confidence = "observed"
		r.Reasons = append(r.Reasons, "all tested legs completed with full recipient credit")
	}
	if len(r.Provider.Disagreements) > 0 {
		r.Reasons = append(r.Reasons, r.Provider.Disagreements...)
		if r.Mark == "NORMAL" || r.Mark == "NOTICE" {
			r.Mark = "WARNING"
		}
	}
}
