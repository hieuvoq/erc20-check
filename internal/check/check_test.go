package check

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func example() Report {
	r := Report{Chain: 1, Token: mainUSDC, Pool: "pool-A", Router: mainRouter, Amount: "100", Block: 10, Hash: "0x" + strings.Repeat("a", 64), Threshold: 1000, Legs: map[string]Leg{}}
	for _, k := range required {
		r.Legs[k] = measure(big.NewInt(10000), big.NewInt(10000), big.NewInt(10000), "")
	}
	r.Legs["approve"] = Leg{Status: "ok"}
	return r
}
func observedRestriction() Report {
	r := example()
	for _, k := range []string{"sell_partial", "sell_all", "pool_transfer"} {
		r.Legs[k] = Leg{Status: "reverted"}
	}
	r.Legs["control_roundtrip"] = Leg{Status: "ok"}
	return r
}
func TestMarks(t *testing.T) {
	tests := []struct {
		name, want string
		change     func(*Report)
	}{
		{"all clear", "NORMAL", func(*Report) {}},
		{"fee", "NOTICE", func(r *Report) { r.Legs["buy"] = measure(big.NewInt(10000), big.NewInt(10000), big.NewInt(9700), "") }},
		{"inclusive high fee", "WARNING", func(r *Report) {
			r.Legs["transfer"] = measure(big.NewInt(10000), big.NewInt(10000), big.NewInt(9000), "")
		}},
		{"full sale rejected", "WARNING", func(r *Report) { r.Legs["sell_all"] = Leg{Status: "reverted"} }},
		{"rpc error", "UNKNOWN", func(r *Report) { r.Legs["sell_all"] = Leg{Status: "unknown"} }},
		{"ambiguous debit", "UNKNOWN", func(r *Report) { r.Legs["buy"] = measure(big.NewInt(10000), big.NewInt(10500), big.NewInt(10000), "") }},
		{"snapshot error", "UNKNOWN", func(r *Report) { r.ProbeError = "restore failed" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := example()
			test.change(&r)
			classify(&r, nil)
			if r.Mark != test.want {
				t.Fatalf("%s: %v", r.Mark, r.Reasons)
			}
		})
	}
}
func TestEscalation(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		edit       func(*Report)
	}{
		{"same block", "WARNING", func(*Report) {}},
		{"later independent block", "CRITICAL", func(r *Report) { r.Block++; r.Hash = "0x" + strings.Repeat("b", 64) }},
		{"different pool", "WARNING", func(r *Report) { r.Block++; r.Hash = "0x" + strings.Repeat("b", 64); r.Pool = "pool-B" }},
		{"control failed", "WARNING", func(r *Report) {
			r.Block++
			r.Hash = "0x" + strings.Repeat("b", 64)
			r.Legs["control_roundtrip"] = Leg{Status: "unknown"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prior := observedRestriction()
			classify(&prior, nil)
			r := observedRestriction()
			tc.edit(&r)
			classify(&r, &prior)
			if r.Mark != tc.want {
				t.Fatal(r.Mark, r.Reasons)
			}
		})
	}
}
func TestGoPlus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("contract_addresses") != mainUSDC || r.URL.Path != "/1" || r.Header.Get("Authorization") != "Bearer test" {
			t.Error("unexpected provider request")
		}
		fmt.Fprintf(w, `{"code":1,"result":{"%s":{"buy_tax":"","sell_tax":"0.03","transfer_tax":null,"cannot_sell_all":"1"}}}`, mainUSDC)
	}))
	defer server.Close()
	r := example()
	r.Provider = fetchProvider(context.Background(), server.URL, 1, mainUSDC, "test")
	compare(&r)
	classify(&r, nil)
	if r.Provider.Status != "available" || r.Mark != "WARNING" || len(r.Provider.Disagreements) != 3 {
		t.Fatal(r.Provider, r.Mark)
	}
	if _, ok := r.Provider.Fields["buy_tax"]; ok {
		t.Fatal("empty provider tax interpreted as known")
	}
	// A blocked simulation has no local tax estimate, even if the provider reports 100%.
	r = example()
	r.Legs["sell_all"] = Leg{Status: "reverted"}
	r.Provider = Provider{Status: "available", Fields: map[string]string{"sell_tax": "1"}}
	compare(&r)
	classify(&r, nil)
	if r.Legs["sell_all"].TaxBPS != nil || r.Mark == "CRITICAL" {
		t.Fatal(r)
	}
}
