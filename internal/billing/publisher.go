package billing

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/cisco/rpc-node-gateway/internal/config"
	"github.com/cisco/rpc-node-gateway/internal/db"
	"github.com/cisco/rpc-node-gateway/internal/model"
	"github.com/cisco/rpc-node-gateway/internal/store"
	"github.com/redis/go-redis/v9"
)

// Publisher 异步计费/用量事件发布接口。
type Publisher interface {
	Publish(ctx context.Context, ev model.BillingEvent) error
	Close() error
}

type Options struct {
	Config config.BillingConfig
	Redis  *store.Redis
	DB     *db.Postgres // 可选；非 nil 时写入 billing_events / usage_daily
}

func NewPublisher(opts Options) Publisher {
	var pubs []Publisher

	switch opts.Config.Publisher {
	case "redis_stream":
		if opts.Redis != nil && opts.Redis.Available() {
			pubs = append(pubs, &RedisStreamPublisher{rdb: opts.Redis, key: opts.Config.StreamKey})
		} else {
			pubs = append(pubs, &LogPublisher{})
		}
	case "postgres":
		// postgres 主存储；下面若 DB 可用会再挂 PostgresPublisher
		if opts.DB == nil {
			pubs = append(pubs, &LogPublisher{})
		}
	default: // log
		pubs = append(pubs, &LogPublisher{})
	}

	if opts.DB != nil {
		pubs = append(pubs, &PostgresPublisher{db: opts.DB})
	}

	if len(pubs) == 1 {
		return pubs[0]
	}
	return MultiPublisher(pubs)
}

type LogPublisher struct{}

func (p *LogPublisher) Publish(_ context.Context, ev model.BillingEvent) error {
	slog.Info("billing_event",
		"event_id", ev.EventID,
		"token", ev.TokenKey,
		"user_id", ev.UserID,
		"plan", ev.Plan,
		"chain", ev.ChainID,
		"methods", ev.Methods,
		"success_n", ev.SuccessN,
		"billable", ev.Billable,
		"amount_cents", ev.AmountCents,
		"transport", ev.Transport,
		"event_kind", ev.EventKind,
		"upstream", ev.Upstream,
		"latency_ms", ev.LatencyMs,
		"ip", ev.ClientIP,
	)
	return nil
}

func (p *LogPublisher) Close() error { return nil }

type RedisStreamPublisher struct {
	rdb *store.Redis
	key string
}

func (p *RedisStreamPublisher) Publish(ctx context.Context, ev model.BillingEvent) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return p.rdb.Client.XAdd(ctx, &redis.XAddArgs{
		Stream: p.key,
		Values: map[string]interface{}{
			"payload": string(payload),
		},
	}).Err()
}

func (p *RedisStreamPublisher) Close() error { return nil }

// PostgresPublisher 将成功调用写入 PostgreSQL 账本表。
type PostgresPublisher struct {
	db *db.Postgres
}

func (p *PostgresPublisher) Publish(ctx context.Context, ev model.BillingEvent) error {
	if ev.Billable {
		return p.db.RecordBillableSuccess(ctx, ev)
	}
	// 免费 token：只累计 usage_daily，不进 billing_events 计费流水
	return p.db.IncrUsageDaily(ctx, ev.TokenKey, ev.At, ev.SuccessN, 0)
}

func (p *PostgresPublisher) Close() error { return nil }

// MultiPublisher 顺序发给多个下游（日志 + DB 等）。
type MultiPublisher []Publisher

func (m MultiPublisher) Publish(ctx context.Context, ev model.BillingEvent) error {
	var firstErr error
	for _, p := range m {
		if err := p.Publish(ctx, ev); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m MultiPublisher) Close() error {
	for _, p := range m {
		_ = p.Close()
	}
	return nil
}
