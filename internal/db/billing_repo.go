package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cisco/rpc-node-gateway/internal/model"
	"github.com/jackc/pgx/v5"
)

// InsertBillingEvent 幂等写入计费事件；仅应写入 billable=true 的事件。
func (p *Postgres) InsertBillingEvent(ctx context.Context, ev model.BillingEvent) error {
	methods, err := json.Marshal(ev.Methods)
	if err != nil {
		return err
	}
	var userID *string
	if ev.UserID != "" {
		userID = &ev.UserID
	}
	_, err = p.Pool.Exec(ctx, `
INSERT INTO billing_events (
    event_id, token_key, user_id, plan_code, chain_id, methods,
    success_n, billable, amount_cents, upstream, latency_ms, client_ip, created_at
) VALUES (
    $1, $2, $3::uuid, $4, $5, $6::jsonb,
    $7, $8, $9, $10, $11, $12, $13
)
ON CONFLICT (event_id) DO NOTHING
`,
		ev.EventID,
		ev.TokenKey,
		userID,
		ev.Plan,
		ev.ChainID,
		methods,
		ev.SuccessN,
		ev.Billable,
		ev.AmountCents,
		ev.Upstream,
		ev.LatencyMs,
		ev.ClientIP,
		ev.At,
	)
	if err != nil {
		return fmt.Errorf("insert billing_events: %w", err)
	}
	return nil
}

// IncrUsageDaily 累加日用量（成功次数 / 计费次数）。
func (p *Postgres) IncrUsageDaily(ctx context.Context, tokenKey string, day time.Time, successN, billableN int) error {
	if successN <= 0 && billableN <= 0 {
		return nil
	}
	d := day.UTC().Format("2006-01-02")
	_, err := p.Pool.Exec(ctx, `
INSERT INTO usage_daily (token_key, day, success_count, billable_count, updated_at)
VALUES ($1, $2::date, $3, $4, now())
ON CONFLICT (token_key, day) DO UPDATE SET
    success_count  = usage_daily.success_count + EXCLUDED.success_count,
    billable_count = usage_daily.billable_count + EXCLUDED.billable_count,
    updated_at     = now()
`, tokenKey, d, successN, billableN)
	if err != nil {
		return fmt.Errorf("incr usage_daily: %w", err)
	}
	return nil
}

// RecordBillableSuccess 在同一事务中写入事件并累加日用量。
func (p *Postgres) RecordBillableSuccess(ctx context.Context, ev model.BillingEvent) error {
	tx, err := p.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	methods, err := json.Marshal(ev.Methods)
	if err != nil {
		return err
	}
	var userID *string
	if ev.UserID != "" {
		userID = &ev.UserID
	}

	tag, err := tx.Exec(ctx, `
INSERT INTO billing_events (
    event_id, token_key, user_id, plan_code, chain_id, methods,
    success_n, billable, amount_cents, upstream, latency_ms, client_ip, created_at
) VALUES (
    $1, $2, $3::uuid, $4, $5, $6::jsonb,
    $7, $8, $9, $10, $11, $12, $13
)
ON CONFLICT (event_id) DO NOTHING
`,
		ev.EventID, ev.TokenKey, userID, ev.Plan, ev.ChainID, methods,
		ev.SuccessN, ev.Billable, ev.AmountCents, ev.Upstream, ev.LatencyMs, ev.ClientIP, ev.At,
	)
	if err != nil {
		return fmt.Errorf("insert billing_events: %w", err)
	}
	// 仅首次插入时累加用量，保证幂等
	if tag.RowsAffected() > 0 {
		billableN := 0
		if ev.Billable {
			billableN = ev.SuccessN
		}
		d := ev.At.UTC().Format("2006-01-02")
		if _, err := tx.Exec(ctx, `
INSERT INTO usage_daily (token_key, day, success_count, billable_count, updated_at)
VALUES ($1, $2::date, $3, $4, now())
ON CONFLICT (token_key, day) DO UPDATE SET
    success_count  = usage_daily.success_count + EXCLUDED.success_count,
    billable_count = usage_daily.billable_count + EXCLUDED.billable_count,
    updated_at     = now()
`, ev.TokenKey, d, ev.SuccessN, billableN); err != nil {
			return fmt.Errorf("incr usage_daily: %w", err)
		}
	}
	return tx.Commit(ctx)
}
