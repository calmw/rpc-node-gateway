# Statistics

网关在进程内收集请求统计，并通过 Admin API 查询：有哪些 Token、每个 Token 的请求情况、以及全局汇总。

当前为 **单机内存统计**（重启清零）。多实例部署时各看各的；后续可再接到 Redis/Prometheus 做聚合。

统计类接口使用 **JWT 鉴权**。JWT 由进程内定时任务签发，并写入数据库表 `admin_jwt_tokens`（未开库时写入内存）。

## Metrics

| Field | Meaning |
|-------|---------|
| `requests` | 请求次数（含鉴权后被拒绝的） |
| `proxied` | 实际转发上游次数 |
| `success_calls` | JSON-RPC 成功条数（batch 可按条累加） |
| `rpc_errors` | 上游返回的 JSON-RPC error 条数 |
| `upstream_errors` | 上游不可用 / HTTP 5xx |
| `auth_failed` | Token 无效或缺失 |
| `method_denied` | 方法黑名单拦截 |
| `rate_limited` | 限流拒绝 |
| `domain_rejected` | Host 未绑定 |
| `billable_calls` | 计费成功条数 |
| `free_calls` | 免费 Token 成功条数 |
| `ws_connections` | WebSocket 建连次数 |
| `ws_notifications` | WS 推送原始条数（折算进 `success_calls`） |

每个 Token 还带 `by_chain` 分链统计，以及 `last_request_at`。WS 计量说明见 [websocket.md](websocket.md)。

## JWT Auth

### Config

```yaml
admin:
  enabled: true
  jwt:
    secret: "change-me-admin-jwt-secret"
    issuer: "rpc-node-gateway"
    subject: "admin-stats"
    ttl: 24h
    rotate_every: 1h
    rotate_on_start: true
    revoke_previous: true
    cleanup_retention: 168h
    log_token: true   # 本地可 true；生产 false，从 DB 取 token
```

### Migration

```bash
psql "$DATABASE_URL" -f migrations/003_admin_jwt.sql
```

并设置 `database.enabled: true`，JWT 会持久化到 `admin_jwt_tokens`。

### 获取当前有效 JWT

```sql
SELECT token, jti, expires_at
FROM admin_jwt_tokens
WHERE revoked = false AND expires_at > now()
ORDER BY issued_at DESC
LIMIT 1;
```

或本地打开 `log_token: true`，启动/轮换时日志字段 `admin jwt token ready`。

### 调用方式

```bash
export ADMIN_JWT='eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...'

curl -s http://127.0.0.1:8080/admin/stats \
  -H "Authorization: Bearer $ADMIN_JWT"

curl -s http://127.0.0.1:8080/admin/stats/tokens/demo-pro-token \
  -H "Authorization: Bearer $ADMIN_JWT"
```

校验逻辑：HS256 签名 + `iss`/`sub` + `exp`，并核对库中 `jti` 未吊销、未过期。

## API

| Method | Path | Description |
|--------|------|-------------|
| GET | `/admin/stats` | 全局汇总 + 全部 Token |
| GET | `/admin/stats/tokens` | Token 列表及各自指标 |
| GET | `/admin/stats/tokens/{token}` | 单个 Token 详情 |
| GET | `/admin/tokens` | 同 `/admin/stats/tokens` |

`/healthz` 与 `/admin/*` **不受** `server.domains` 限制。

### Sample response (`/admin/stats`)

```json
{
  "generated_at": "2026-07-15T13:30:00Z",
  "total": {
    "requests": 10,
    "proxied": 8,
    "success_calls": 7,
    "rpc_errors": 1,
    "upstream_errors": 0,
    "auth_failed": 1,
    "method_denied": 1,
    "rate_limited": 0,
    "domain_rejected": 0,
    "billable_calls": 5,
    "free_calls": 2
  },
  "token_count": 2,
  "tokens": [
    {
      "token_key": "demo-free-token",
      "token_name": "demo-free",
      "plan": "free",
      "billing_free": true,
      "enabled": true,
      "last_request_at": "2026-07-15T13:29:50Z",
      "metrics": { "requests": 3, "success_calls": 2, "free_calls": 2 },
      "by_chain": {
        "eth": { "requests": 3, "success_calls": 2, "free_calls": 2 }
      }
    }
  ]
}
```

## Implementation

- 采集：`internal/stats`
- 接口：`internal/admin`
- JWT 签发/校验/轮换：`internal/adminjwt`
- 表：`migrations/003_admin_jwt.sql`
