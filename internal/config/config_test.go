package config_test

import (
	"testing"

	"github.com/cisco/rpc-node-gateway/internal/config"
)

func TestHostAllowed(t *testing.T) {
	domains := []string{"rpc.example.com", "localhost", "127.0.0.1"}
	if !config.HostAllowed(domains, "rpc.example.com") {
		t.Fatal("expected allow")
	}
	if !config.HostAllowed(domains, "RPC.Example.com:8080") {
		t.Fatal("expected allow with port")
	}
	if config.HostAllowed(domains, "evil.com") {
		t.Fatal("expected deny")
	}
	if !config.HostAllowed(nil, "anything.com") {
		t.Fatal("empty domains should allow all")
	}
}
