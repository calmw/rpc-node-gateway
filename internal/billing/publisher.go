package billing

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/cisco/rpc-node-gateway/internal/config"
	"github.com/cisco/rpc-node-gateway/internal/model"
	"github.com/cisco/rpc-node-gateway/internal/store"
	"github.com/redis/go-redis/v9"
)

// Publisher 异步计费事件发布接口（仅 billable=true 的事件应被调用方传入）。
type Publisher interface {
	Publish(ctx context.Context, ev model.BillingEvent) error
	Close() error
}

func NewPublisher(cfg config.BillingConfig, rdb *store.Redis) Publisher {
	if cfg.Publisher == "redis_stream" && rdb.Available() {
		return &RedisStreamPublisher{rdb: rdb, key: cfg.StreamKey}
	}
	return &LogPublisher{}
}

type LogPublisher struct{}

func (p *LogPublisher) Publish(_ context.Context, ev model.BillingEvent) error {
	slog.Info("billing_event",
		"event_id", ev.EventID,
		"token", ev.TokenKey,
		"plan", ev.Plan,
		"chain", ev.ChainID,
		"methods", ev.Methods,
		"success_n", ev.SuccessN,
		"billable", ev.Billable,
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
