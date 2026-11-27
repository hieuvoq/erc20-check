package check

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

const mainRouter = "0x7a250d5630b4cf539739df2c5dacb4c659f2488d"
const mainUSDC = "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
const mainDAI = "0x6b175474e89094c44da98b954eedeac495271d0f"
const swapTopic = "0xd78ad95fa46c994b6551d0da85fc275fe613ce37657fb8d5e3d130840159d822"

func word(n *big.Int) string { return fmt.Sprintf("%064x", n) }
func addr(a string) string   { return strings.Repeat("0", 24) + strings.ToLower(a[2:]) }
func uintArg(n int64) string { return word(big.NewInt(n)) }
func words(data string, count int) ([]*big.Int, error) {
	if !strings.HasPrefix(data, "0x") || len(data) != 2+64*count {
		return nil, errors.New("unexpected ABI return length")
	}
	ns := make([]*big.Int, count)
	for i := range ns {
		v, err := number("0x" + data[2+64*i:2+64*(i+1)])
		if err != nil {
			return nil, err
		}
		ns[i] = v
	}
	return ns, nil
}

type node struct {
	rpc
	ctx context.Context
}

func (n node) read(to, calldata string) (string, error) {
	return scalar(n.ctx, n.rpc, "eth_call", map[string]string{"to": to, "data": calldata}, "latest")
}
func (n node) address(to, data string) (string, error) {
	raw, err := n.read(to, data)
	if err != nil {
		return "", err
	}
	if len(raw) != 66 || raw[:26] != "0x"+strings.Repeat("0", 24) || !addressRE.MatchString("0x"+raw[26:]) {
		return "", errors.New("invalid ABI address")
	}
	return strings.ToLower("0x" + raw[26:]), nil
}
func (n node) uint(to, data string) (*big.Int, error) {
	v, err := n.read(to, data)
	if err != nil {
		return nil, err
	}
	vals, err := words(v, 1)
	if err != nil {
		return nil, err
	}
	return vals[0], nil
}
func (n node) balance(token, who string) (*big.Int, error) {
	return n.uint(token, "0x70a08231"+addr(who))
}

type logEntry struct {
	Address, Data string
	Topics        []string
}
type receipt struct {
	Status, GasUsed string
	Logs            []logEntry
}
type reverted struct{ hash string }

func (e reverted) Error() string { return "fork transaction reverted" }
func (n node) send(from, to, data string, value *big.Int) (string, receipt, error) {
	var rec receipt
	tx := map[string]string{"from": from, "to": to, "data": data, "gas": "0x5b8d80"}
	if value != nil {
		tx["value"] = "0x" + value.Text(16)
	}
	h, err := scalar(n.ctx, n.rpc, "eth_sendTransaction", tx)
	if err != nil {
		return "", rec, err
	}
	if !hashRE.MatchString(h) {
		return "", rec, errors.New("invalid transaction hash")
	}
	wait, cancel := context.WithTimeout(n.ctx, 40*time.Second)
	defer cancel()
	for {
		err = n.call(wait, "eth_getTransactionReceipt", []any{h}, &rec)
		if !errors.Is(err, errNoResult) {
			break
		}
		select {
		case <-wait.Done():
			return h, rec, fmt.Errorf("fork receipt unavailable: %w", wait.Err())
		case <-time.After(120 * time.Millisecond):
		}
	}
	if err != nil {
		return h, rec, err
	}
	switch rec.Status {
	case "0x1":
		return h, rec, nil
	case "0x0":
		if rec.GasUsed == "0x5b8d80" {
			return h, rec, errors.New("fork gas limit exhausted")
		}
		return h, rec, reverted{h}
	default:
		return h, rec, errors.New("missing receipt status")
	}
}
func (n node) isolated(fn func() error) error {
	id, err := scalar(n.ctx, n.rpc, "evm_snapshot")
	if err != nil {
		return err
	}
	outcome := fn()
	var ok bool
	if err = n.call(n.ctx, "evm_revert", []any{id}, &ok); err != nil || !ok {
		return errors.New("failed to restore fork snapshot")
	}
	return outcome
}
func swapData(amount *big.Int, path []string, recipient string) string {
	// Router02.swapExactTokensForTokensSupportingFeeOnTransferTokens.
	// Offset 160 points past the five ABI head words to address[].
	data := "0x5c11d795" + word(amount) + uintArg(0) + uintArg(160) + addr(recipient) + strings.Repeat("f", 64) + uintArg(int64(len(path)))
	for _, a := range path {
		data += addr(a)
	}
	return data
}
func reverse(path []string) []string {
	r := make([]string, len(path))
	for i, a := range path {
		r[len(path)-1-i] = a
	}
	return r
}
func now() string { return time.Now().UTC().Format(time.RFC3339) }
