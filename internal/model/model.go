package model

import "time"

// Token 表示一次请求绑定的 API 凭证与套餐信息。
type Token struct {
	Key  string
	Name string
	Plan string

	UserID               string // DB 用户 ID；YAML 模式可为空
	PricePerSuccessCents int    // 套餐单价（分）；免费计费 token 仍可能有单价但 BillingFree 跳过

	Enabled     bool
	BillingFree bool // true=成功调用不计费，但仍限流

	// Token 全局限流（不分 IP）
	TokenRateLimitPerSecond int
	TokenRateLimitBurst     int
	// Token + IP 限流
	TokenIPRateLimitPerSecond int
	TokenIPRateLimitBurst     int

	DailyQuota    int64
	DeniedMethods map[string]struct{}
}

type BillingEvent struct {
	EventID     string    `json:"event_id"`
	TokenKey    string    `json:"token_key"`
	TokenName   string    `json:"token_name"`
	UserID      string    `json:"user_id,omitempty"`
	Plan        string    `json:"plan"`
	ChainID     string    `json:"chain_id"`
	Methods     []string  `json:"methods"`
	SuccessN    int       `json:"success_n"`
	Billable    bool      `json:"billable"`
	AmountCents int       `json:"amount_cents"`
	Transport   string    `json:"transport"`  // http | ws
	EventKind   string    `json:"event_kind"` // rpc | ws_notification
	Upstream    string    `json:"upstream"`
	LatencyMs   int64     `json:"latency_ms"`
	ClientIP    string    `json:"client_ip"`
	At          time.Time `json:"at"`
}

type RateLimitLog struct {
	TokenKey  string    `json:"token_key"`
	TokenName string    `json:"token_name"`
	ChainID   string    `json:"chain_id"`
	ClientIP  string    `json:"client_ip"`
	Reason    string    `json:"reason"` // token_rate_limit | token_ip_rate_limit | daily_quota
	At        time.Time `json:"at"`
}
