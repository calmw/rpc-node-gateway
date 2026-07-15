package jsonrpc_test

import (
	"testing"

	"github.com/cisco/rpc-node-gateway/internal/jsonrpc"
)

func TestParseMethodsSingle(t *testing.T) {
	methods, err := jsonrpc.ParseMethods([]byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 1 || methods[0] != "eth_blockNumber" {
		t.Fatalf("unexpected methods: %#v", methods)
	}
}

func TestParseMethodsBatch(t *testing.T) {
	body := `[{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1},{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":2}]`
	methods, err := jsonrpc.ParseMethods([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 {
		t.Fatalf("want 2 methods, got %d", len(methods))
	}
}

func TestCountSuccess(t *testing.T) {
	ok := jsonrpc.CountSuccess([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	if ok != 1 {
		t.Fatalf("want 1, got %d", ok)
	}
	fail := jsonrpc.CountSuccess([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"err"}}`))
	if fail != 0 {
		t.Fatalf("want 0, got %d", fail)
	}
	batch := jsonrpc.CountSuccess([]byte(`[
		{"jsonrpc":"2.0","id":1,"result":"0x1"},
		{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"err"}}
	]`))
	if batch != 1 {
		t.Fatalf("want 1, got %d", batch)
	}
}
