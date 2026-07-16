package stats_test

import (
	"testing"

	"github.com/cisco/rpc-node-gateway/internal/stats"
)

func TestCollectorOverview(t *testing.T) {
	c := stats.NewCollector()
	meta := stats.TokenMeta{Key: "t1", Name: "n1", Plan: "free", BillingFree: true, Enabled: true}
	c.RegisterTokens([]stats.TokenMeta{meta})
	c.RecordProxy(meta, "eth", 2, 0, false, false)
	c.IncRateLimited(meta)

	ov := c.Snapshot(nil)
	if ov.Total.Requests != 2 {
		t.Fatalf("total requests=%d want 2", ov.Total.Requests)
	}
	if ov.Total.SuccessCalls != 2 {
		t.Fatalf("success=%d", ov.Total.SuccessCalls)
	}
	if ov.Total.RateLimited != 1 {
		t.Fatalf("rate_limited=%d", ov.Total.RateLimited)
	}
	if ov.TokenCount != 1 {
		t.Fatalf("token_count=%d", ov.TokenCount)
	}
	tok := ov.Tokens[0]
	if tok.Metrics.FreeCalls != 2 {
		t.Fatalf("free_calls=%d", tok.Metrics.FreeCalls)
	}
	if tok.ByChain["eth"].SuccessCalls != 2 {
		t.Fatalf("by_chain eth success=%d", tok.ByChain["eth"].SuccessCalls)
	}
}
