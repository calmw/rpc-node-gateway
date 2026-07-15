package filter_test

import (
	"testing"

	"github.com/cisco/rpc-node-gateway/internal/filter"
	"github.com/cisco/rpc-node-gateway/internal/model"
)

func TestCheckMethods(t *testing.T) {
	token := &model.Token{
		DeniedMethods: map[string]struct{}{
			"eth_sendRawTransaction": {},
		},
	}
	if err := filter.CheckMethods(token, []string{"eth_blockNumber"}); err != nil {
		t.Fatalf("unexpected deny: %v", err)
	}
	if err := filter.CheckMethods(token, []string{"eth_sendRawTransaction"}); err == nil {
		t.Fatal("expected deny")
	}
}
