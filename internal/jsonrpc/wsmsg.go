package jsonrpc

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// MessageKind 区分 WS/HTTP 上的 JSON-RPC 消息类型。
type MessageKind int

const (
	KindRequest MessageKind = iota
	KindResponse
	KindNotification // 无 id 的推送，如 eth_subscription
	KindUnknown
)

// Envelope 用于分类与提取关键字段。
type Envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	ID      json.RawMessage `json:"id"`
	Error   json.RawMessage `json:"error"`
	Result  json.RawMessage `json:"result"`
	Params  json.RawMessage `json:"params"`
}

func Classify(body []byte) MessageKind {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return KindUnknown
	}
	// batch：按请求/响应处理（推送一般不会 batch）
	if body[0] == '[' {
		return KindRequest // 调用方再细分；出站 batch 当响应
	}
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return KindUnknown
	}
	hasID := len(env.ID) > 0 && string(env.ID) != "null"
	if env.Method != "" && !hasID {
		return KindNotification
	}
	if env.Method != "" && hasID {
		return KindRequest
	}
	if hasID && (len(env.Result) > 0 || len(env.Error) > 0 || env.Result != nil || env.Error != nil) {
		return KindResponse
	}
	// result/error 可能为 null，仍算响应
	if hasID {
		return KindResponse
	}
	return KindUnknown
}

// ParseRequestMeta 解析单条请求的 id、method、subscribe 类型。
func ParseRequestMeta(body []byte) (id string, method string, subKind string, err error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return "", "", "", fmt.Errorf("empty body")
	}
	if body[0] == '[' {
		return "", "", "", fmt.Errorf("batch not supported on inspect path")
	}
	var raw struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", "", "", err
	}
	if raw.Method == "" {
		return "", "", "", fmt.Errorf("missing method")
	}
	id = normalizeID(raw.ID)
	subKind = ""
	if raw.Method == "eth_subscribe" || raw.Method == "eth_unsubscribe" {
		subKind = firstStringParam(raw.Params)
	}
	return id, raw.Method, subKind, nil
}

// ParseResponseMeta 解析响应 id、是否成功、result（用于 subscribe id）。
func ParseResponseMeta(body []byte) (id string, success bool, result string, err error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return "", false, "", fmt.Errorf("empty body")
	}
	var raw struct {
		ID     json.RawMessage `json:"id"`
		Error  json.RawMessage `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", false, "", err
	}
	id = normalizeID(raw.ID)
	if len(raw.Error) > 0 && string(raw.Error) != "null" {
		return id, false, "", nil
	}
	res := ""
	if len(raw.Result) > 0 && string(raw.Result) != "null" {
		_ = json.Unmarshal(raw.Result, &res)
		if res == "" {
			res = string(raw.Result)
		}
	}
	return id, true, res, nil
}

// ParseNotification 解析 eth_subscription 推送。
func ParseNotification(body []byte) (method string, subscriptionID string, ok bool) {
	body = bytes.TrimSpace(body)
	var raw struct {
		Method string `json:"method"`
		Params *struct {
			Subscription string          `json:"subscription"`
			Result       json.RawMessage `json:"result"`
		} `json:"params"`
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", "", false
	}
	if len(raw.ID) > 0 && string(raw.ID) != "null" {
		return "", "", false
	}
	if raw.Method == "" {
		return "", "", false
	}
	sub := ""
	if raw.Params != nil {
		sub = raw.Params.Subscription
	}
	return raw.Method, sub, true
}

func normalizeID(id json.RawMessage) string {
	if len(id) == 0 || string(id) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(id, &s); err == nil {
		return s
	}
	var n json.Number
	if err := json.Unmarshal(id, &n); err == nil {
		return n.String()
	}
	return string(id)
}

func firstStringParam(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(params, &arr); err != nil || len(arr) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(arr[0], &s); err == nil {
		return s
	}
	return ""
}
