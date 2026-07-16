package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cisco/rpc-node-gateway/internal/model"
)

// TokenRow 是 api_tokens JOIN plans 的查询结果。
type TokenRow struct {
	TokenKey                  string
	Name                      string
	PlanCode                  string
	Enabled                   bool
	BillingFree               bool
	UserID                    string
	TokenIPRateLimitPerSecond int
	TokenIPRateLimitBurst     int
	TokenRateLimitPerSecond   int
	TokenRateLimitBurst       int
	DailyQuota                int64
	DeniedMethodsJSON         []byte
	PricePerSuccessCents      int
}

func (r TokenRow) ToModel() (*model.Token, error) {
	var deniedList []string
	if len(r.DeniedMethodsJSON) > 0 {
		if err := json.Unmarshal(r.DeniedMethodsJSON, &deniedList); err != nil {
			return nil, fmt.Errorf("parse denied_methods for %s: %w", r.TokenKey, err)
		}
	}
	denied := make(map[string]struct{}, len(deniedList))
	for _, m := range deniedList {
		denied[m] = struct{}{}
	}
	return &model.Token{
		Key:                       r.TokenKey,
		Name:                      r.Name,
		Plan:                      r.PlanCode,
		Enabled:                   r.Enabled,
		BillingFree:               r.BillingFree,
		UserID:                    r.UserID,
		PricePerSuccessCents:      r.PricePerSuccessCents,
		TokenRateLimitPerSecond:   r.TokenRateLimitPerSecond,
		TokenRateLimitBurst:       r.TokenRateLimitBurst,
		TokenIPRateLimitPerSecond: r.TokenIPRateLimitPerSecond,
		TokenIPRateLimitBurst:     r.TokenIPRateLimitBurst,
		DailyQuota:                r.DailyQuota,
		DeniedMethods:             denied,
	}, nil
}

// ListEnabledTokens 加载所有启用 Token 及其套餐限流策略。
func (p *Postgres) ListEnabledTokens(ctx context.Context) ([]*model.Token, error) {
	const q = `
SELECT
    t.token_key,
    t.name,
    t.plan_code,
    t.enabled,
    t.billing_free,
    t.user_id::text,
    p.token_ip_rate_limit_per_second,
    p.token_ip_rate_limit_burst,
    p.token_rate_limit_per_second,
    p.token_rate_limit_burst,
    p.daily_quota,
    p.denied_methods,
    p.price_per_success_cents
FROM api_tokens t
JOIN plans p ON p.code = t.plan_code
WHERE t.enabled = true
`
	rows, err := p.Pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer rows.Close()

	out := make([]*model.Token, 0)
	for rows.Next() {
		var r TokenRow
		if err := rows.Scan(
			&r.TokenKey,
			&r.Name,
			&r.PlanCode,
			&r.Enabled,
			&r.BillingFree,
			&r.UserID,
			&r.TokenIPRateLimitPerSecond,
			&r.TokenIPRateLimitBurst,
			&r.TokenRateLimitPerSecond,
			&r.TokenRateLimitBurst,
			&r.DailyQuota,
			&r.DeniedMethodsJSON,
			&r.PricePerSuccessCents,
		); err != nil {
			return nil, fmt.Errorf("scan token: %w", err)
		}
		tok, err := r.ToModel()
		if err != nil {
			return nil, err
		}
		out = append(out, tok)
	}
	return out, rows.Err()
}
