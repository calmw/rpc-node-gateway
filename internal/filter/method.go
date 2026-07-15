package filter

import (
	"fmt"
	"strings"

	"github.com/cisco/rpc-node-gateway/internal/model"
)

// CheckMethods 若存在禁止方法则返回错误。
func CheckMethods(token *model.Token, methods []string) error {
	if token == nil {
		return fmt.Errorf("token is nil")
	}
	var denied []string
	for _, m := range methods {
		if _, ok := token.DeniedMethods[m]; ok {
			denied = append(denied, m)
		}
	}
	if len(denied) == 0 {
		return nil
	}
	return fmt.Errorf("method(s) not allowed: %s", strings.Join(denied, ", "))
}
