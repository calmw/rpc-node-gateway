package jsonrpc_test

import (
	"testing"

	"github.com/cisco/rpc-node-gateway/internal/jsonrpc"
)

func TestParseNotification(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","method":"eth_subscription","params":{"subscription":"0xabc","result":{"number":"0x1"}}}`)
	method, sub, ok := jsonrpc.ParseNotification(body)
	if !ok || method != "eth_subscription" || sub != "0xabc" {
		t.Fatalf("got method=%s sub=%s ok=%v", method, sub, ok)
	}
}

func TestClassifyResponse(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`)
	if jsonrpc.Classify(body) != jsonrpc.KindResponse {
		t.Fatal("want response")
	}
	req := []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`)
	if jsonrpc.Classify(req) != jsonrpc.KindRequest {
		t.Fatal("want request")
	}
}
