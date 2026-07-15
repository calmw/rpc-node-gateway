package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/cisco/rpc-node-gateway/internal/config"
	"github.com/redis/go-redis/v9"
)

// Redis 封装；Enabled=false 或连接失败时 Client 为 nil，业务侧自行降级。
type Redis struct {
	Client *redis.Client
}

func NewRedis(cfg config.RedisConfig) (*Redis, error) {
	if !cfg.Enabled {
		slog.Info("redis disabled, using in-process fallbacks")
		return &Redis{}, nil
	}
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		slog.Warn("redis unavailable, falling back to in-process limiters", "err", err)
		_ = client.Close()
		return &Redis{}, nil
	}
	slog.Info("redis connected", "addr", cfg.Addr)
	return &Redis{Client: client}, nil
}

func (r *Redis) Available() bool {
	return r != nil && r.Client != nil
}

func (r *Redis) Close() error {
	if r == nil || r.Client == nil {
		return nil
	}
	return r.Client.Close()
}

func DailySuccessKey(tokenKey, day string) string {
	return fmt.Sprintf("rpc:success:%s:%s", tokenKey, day)
}

// RateLimitTokenKey：token 全局限流（不分 IP）
func RateLimitTokenKey(tokenKey string) string {
	return fmt.Sprintf("rpc:ratelimit:token:%s", tokenKey)
}

// RateLimitTokenIPKey：token + IP 限流
func RateLimitTokenIPKey(tokenKey, ip string) string {
	return fmt.Sprintf("rpc:ratelimit:tokenip:%s:%s", tokenKey, ip)
}
