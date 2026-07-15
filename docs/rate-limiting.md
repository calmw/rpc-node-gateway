# Rate Limiting

本文说明 rpc-node-gateway 的限流模型、配置项、判定顺序、拒绝响应与日志字段。

限流对 **所有 Token 生效**，包括 `billing_free: true` 的免费 Token。免费只影响计费，不影响限流。

## Modes

网关同时启用两种 QPS 限流（令牌桶），外加按日成功次数的配额保护：

| Mode | Scope | Config keys | Reject reason |
|------|--------|-------------|---------------|
| Token + IP | 同一 Token 在同一客户端 IP | `token_ip_rate_limit_per_second` / `token_ip_rate_limit_burst` | `token_ip_rate_limit` |
| Token (global) | 同一 Token，不分 IP | `token_rate_limit_per_second` / `token_rate_limit_burst` | `token_rate_limit` |
| Daily quota | 同一 Token 当日成功调用累计 | `daily_quota` | `daily_quota` |

任一维度不通过即拒绝请求。

### Mode 1: Token + IP

限制「某个 Token 从某个 IP 打过来」的速率，防止单 IP 打爆，也避免同一 Token 被多 IP 分散绕过时完全失控（与 Mode 2 配合）。

- Redis key：`rpc:ratelimit:tokenip:{token}:{ip}`
- 客户端 IP 取值顺序：`X-Real-IP` → `X-Forwarded-For`（第一个）→ `RemoteAddr`

### Mode 2: Token global

限制某个 Token 的总 QPS，与来源 IP 无关。多 IP / 多机器共用同一 Token 时共享该桶。

- Redis key：`rpc:ratelimit:token:{token}`

### Daily quota

按 **UTC 自然日** 累计「上游成功且 JSON-RPC 无 `error`」的条数（batch 按成功条数累加）。达到 `daily_quota` 后拒绝新请求。

- Redis key：`rpc:success:{token}:{YYYY-MM-DD}`
- 与是否计费无关：免费 Token 成功调用也会计入配额

`daily_quota <= 0` 时不做日配额限制。对应 rate 配置 `*_per_second <= 0` 时关闭该维度限流。

## Decision order

请求通过鉴权与方法过滤后，限流按以下顺序检查：

```text
1. daily_quota
2. token_rate_limit          (token 全局)
3. token_ip_rate_limit       (token + IP)
```

全部通过才转发上游。

## Configuration

限流参数挂在 **plan** 上，Token 通过 `plan` 引用套餐：

```yaml
plans:
  free:
    token_ip_rate_limit_per_second: 3
    token_ip_rate_limit_burst: 6
    token_rate_limit_per_second: 5
    token_rate_limit_burst: 10
    daily_quota: 10000

tokens:
  - key: demo-free-token
    plan: free
    billing_free: true   # 不计费，仍走上述限流
```

| Field | Meaning |
|-------|---------|
| `*_per_second` | 令牌填充速率（QPS） |
| `*_burst` | 桶容量；未配置时默认等于 `*_per_second` |
| `daily_quota` | 每日成功调用上限 |

完整示例见 [`configs/config.yaml`](../configs/config.yaml)。

## Storage backend

| Redis | Behavior |
|-------|----------|
| `redis.enabled: true` 且可连接 | 使用 Redis 令牌桶 + 日计数（多实例一致） |
| 未启用或连接失败 | 降级为进程内 `golang.org/x/time/rate` 与内存计数 |

生产多副本部署请开启 Redis，否则各实例限流相互独立。

## Reject response

限流拒绝时 HTTP 状态码为 `429`，JSON-RPC error 示例：

```json
{
  "jsonrpc": "2.0",
  "id": null,
  "error": {
    "code": -32005,
    "message": "rate limited: token_ip_rate_limit"
  }
}
```

`message` 中的 reason 可能为：

- `token_rate_limit`
- `token_ip_rate_limit`
- `daily_quota`

限流拒绝 **不计费**。

## Logging

拒绝时输出结构化日志字段 `rate_limited`，例如：

```json
{
  "msg": "rate_limited",
  "token": "demo-free-token",
  "name": "demo-free",
  "chain": "eth",
  "ip": "1.2.3.4",
  "reason": "token_ip_rate_limit",
  "at": "2026-07-15T13:00:00Z"
}
```

## Billing vs rate limit

| | Rate limit | Billing |
|--|------------|---------|
| `billing_free: false` | 生效 | 成功调用发 `billing_event` |
| `billing_free: true` | **仍然生效** | 只记 `success_free`，不进计费流水 |

结论：免费与限流不冲突；限流面向所有 Token。

## Implementation

- 判定逻辑：`internal/ratelimit/limiter.go`
- 中间件：`internal/middleware/middleware.go`（`RateLimit`）
- Redis key：`internal/store/redis.go`
