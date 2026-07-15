package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cisco/rpc-node-gateway/internal/model"
	"github.com/cisco/rpc-node-gateway/internal/store"
	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
)

// Result 描述限流判定结果。
type Result struct {
	Allowed bool
	Reason  string // "" | token_rate_limit | token_ip_rate_limit | daily_quota
}

// Limiter 同时执行两种限流：
// 1) token + IP
// 2) token 全局（不分 IP）
// 另含日成功次数配额。限流对所有 token 生效（含免费 token）。
type Limiter struct {
	rdb *store.Redis

	mu       sync.Mutex
	local    map[string]*rate.Limiter
	daily    map[string]int64 // key = token:day
	onReject func(model.RateLimitLog)
}

func New(rdb *store.Redis, onReject func(model.RateLimitLog)) *Limiter {
	return &Limiter{
		rdb:      rdb,
		local:    make(map[string]*rate.Limiter),
		daily:    make(map[string]int64),
		onReject: onReject,
	}
}

func (l *Limiter) Allow(ctx context.Context, token *model.Token, chainID, clientIP string) Result {
	day := time.Now().UTC().Format("2006-01-02")

	used, err := l.dailyUsed(ctx, token.Key, day)
	if err == nil && token.DailyQuota > 0 && used >= token.DailyQuota {
		l.reject(token, chainID, clientIP, "daily_quota")
		return Result{Allowed: false, Reason: "daily_quota"}
	}

	// 先查 token 全局，再查 token+IP；任一命中即拒绝
	if ok := l.allowBucket(ctx, store.RateLimitTokenKey(token.Key), token.TokenRateLimitPerSecond, token.TokenRateLimitBurst); !ok {
		l.reject(token, chainID, clientIP, "token_rate_limit")
		return Result{Allowed: false, Reason: "token_rate_limit"}
	}
	ipKey := store.RateLimitTokenIPKey(token.Key, clientIP)
	if ok := l.allowBucket(ctx, ipKey, token.TokenIPRateLimitPerSecond, token.TokenIPRateLimitBurst); !ok {
		l.reject(token, chainID, clientIP, "token_ip_rate_limit")
		return Result{Allowed: false, Reason: "token_ip_rate_limit"}
	}
	return Result{Allowed: true}
}

func (l *Limiter) IncrSuccess(ctx context.Context, tokenKey string, n int) {
	if n <= 0 {
		return
	}
	day := time.Now().UTC().Format("2006-01-02")
	if l.rdb.Available() {
		key := store.DailySuccessKey(tokenKey, day)
		_ = l.rdb.Client.IncrBy(ctx, key, int64(n)).Err()
		_ = l.rdb.Client.Expire(ctx, key, 48*time.Hour).Err()
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.daily[tokenKey+":"+day] += int64(n)
}

func (l *Limiter) dailyUsed(ctx context.Context, tokenKey, day string) (int64, error) {
	if l.rdb.Available() {
		n, err := l.rdb.Client.Get(ctx, store.DailySuccessKey(tokenKey, day)).Int64()
		if err == redis.Nil {
			return 0, nil
		}
		return n, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.daily[tokenKey+":"+day], nil
}

func (l *Limiter) allowBucket(ctx context.Context, key string, rps, burst int) bool {
	if rps <= 0 {
		return true
	}
	if burst <= 0 {
		burst = rps
	}
	if l.rdb.Available() {
		ok, err := l.redisAllow(ctx, key, rps, burst)
		if err != nil {
			return l.localAllow(key, rps, burst)
		}
		return ok
	}
	return l.localAllow(key, rps, burst)
}

func (l *Limiter) redisAllow(ctx context.Context, key string, rps, burst int) (bool, error) {
	const script = `
local key = KEYS[1]
local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local data = redis.call("HMGET", key, "tokens", "ts")
local tokens = tonumber(data[1])
local ts = tonumber(data[2])
if tokens == nil then
  tokens = burst
  ts = now
end
local delta = math.max(0, now - ts)
local fill = delta * rate
tokens = math.min(burst, tokens + fill)
local allowed = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
end
redis.call("HMSET", key, "tokens", tokens, "ts", now)
redis.call("EXPIRE", key, 2)
return allowed
`
	now := float64(time.Now().UnixMilli()) / 1000.0
	res, err := l.rdb.Client.Eval(ctx, script, []string{key}, rps, burst, now).Int()
	if err != nil {
		if err == redis.Nil {
			return false, nil
		}
		return false, fmt.Errorf("redis rate limit: %w", err)
	}
	return res == 1, nil
}

func (l *Limiter) localAllow(key string, rps, burst int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	lim, ok := l.local[key]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(rps), burst)
		l.local[key] = lim
	}
	return lim.Allow()
}

func (l *Limiter) reject(token *model.Token, chainID, clientIP, reason string) {
	if l.onReject == nil {
		return
	}
	l.onReject(model.RateLimitLog{
		TokenKey:  token.Key,
		TokenName: token.Name,
		ChainID:   chainID,
		ClientIP:  clientIP,
		Reason:    reason,
		At:        time.Now().UTC(),
	})
}
