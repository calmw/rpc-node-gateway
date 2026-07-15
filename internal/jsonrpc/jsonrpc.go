package jsonrpc

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Request 是精简后的 JSON-RPC 请求视图，只关心 method。
type Request struct {
	Method string          `json:"method"`
	ID     json.RawMessage `json:"id"`
}

// Response 用于判断是否计费成功。
type Response struct {
	Error *json.RawMessage `json:"error"`
	ID    json.RawMessage  `json:"id"`
}

// ParseMethods 从 body 提取 method 列表，支持单条与 batch。
func ParseMethods(body []byte) ([]string, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, fmt.Errorf("empty body")
	}
	if body[0] == '[' {
		var batch []Request
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("invalid json-rpc batch: %w", err)
		}
		if len(batch) == 0 {
			return nil, fmt.Errorf("empty json-rpc batch")
		}
		methods := make([]string, 0, len(batch))
		for i, item := range batch {
			if item.Method == "" {
				return nil, fmt.Errorf("batch[%d]: missing method", i)
			}
			methods = append(methods, item.Method)
		}
		return methods, nil
	}

	var single Request
	if err := json.Unmarshal(body, &single); err != nil {
		return nil, fmt.Errorf("invalid json-rpc request: %w", err)
	}
	if single.Method == "" {
		return nil, fmt.Errorf("missing method")
	}
	return []string{single.Method}, nil
}

// CountSuccess 统计响应中无 error 的条数，支持单条与 batch。
func CountSuccess(body []byte) int {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return 0
	}
	if body[0] == '[' {
		var batch []Response
		if err := json.Unmarshal(body, &batch); err != nil {
			return 0
		}
		n := 0
		for _, item := range batch {
			if item.Error == nil {
				n++
			}
		}
		return n
	}
	var single Response
	if err := json.Unmarshal(body, &single); err != nil {
		return 0
	}
	if single.Error == nil {
		return 1
	}
	return 0
}
