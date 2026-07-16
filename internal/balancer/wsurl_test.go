package balancer_test

import (
	"testing"

	"github.com/cisco/rpc-node-gateway/internal/balancer"
)

func TestDeriveWSURL(t *testing.T) {
	if got := balancer.DeriveWSURL("https://example.com/rpc", ""); got != "wss://example.com/rpc" {
		t.Fatalf("got %s", got)
	}
	if got := balancer.DeriveWSURL("http://example.com", "wss://explicit"); got != "wss://explicit" {
		t.Fatalf("got %s", got)
	}
}
