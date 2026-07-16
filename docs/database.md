# Database

网关使用 **PostgreSQL** 持久化用户、Token、套餐与计费账本。限流热数据仍在 Redis（或进程内），不进库。

本地默认 `database.enabled: false`，Token 继续读 YAML，无需装库即可跑通。

## When you need it

| 场景 | 是否需要 DB |
|------|-------------|
| 本地演示 / 内测 | 否（YAML） |
| 开通用户、改套餐、标记免费 Token | 是 |
| 按成功次数出账、对账 | 是 |

## Schema

迁移文件：

- [`migrations/001_init.sql`](../migrations/001_init.sql) — 表结构
- [`migrations/002_seed.sql`](../migrations/002_seed.sql) — 演示用户 / 套餐 / Token
- [`migrations/003_admin_jwt.sql`](../migrations/003_admin_jwt.sql) — Admin JWT 表

| Table | Purpose |
|-------|---------|
| `users` | 用户账号 |
| `plans` | 套餐：双限流、日配额、方法黑名单、单价 |
| `api_tokens` | API Token（`billing_free` 控制是否计费） |
| `usage_daily` | 日成功 / 计费次数汇总 |
| `billing_events` | 幂等计费流水（`event_id` 主键） |
| `billing_ledger` | 账期结算骨架（后续出账用） |
| `admin_jwt_tokens` | Admin 统计接口 JWT（定时任务签发） |

```text
users 1──* api_tokens *──1 plans
              │
              ├── usage_daily
              └── billing_events (billable=true)
```

## Apply migrations

```bash
createdb rpc_gateway   # 或使用已有库

export DATABASE_URL='postgres://postgres:postgres@127.0.0.1:5432/rpc_gateway?sslmode=disable'
psql "$DATABASE_URL" -f migrations/001_init.sql
psql "$DATABASE_URL" -f migrations/002_seed.sql
```

## Enable in config

```yaml
database:
  enabled: true
  dsn: "postgres://postgres:postgres@127.0.0.1:5432/rpc_gateway?sslmode=disable"
  token_refresh: 30s   # 定时从 DB 刷新内存 Token 缓存

billing:
  publisher: log       # 或 postgres / redis_stream
  # database.enabled=true 时，成功调用会额外写入 PG（账本）
```

启用后：

1. Token **从 DB 加载**到内存，并按 `token_refresh` 热更新  
2. YAML 里的 `plans` / `tokens` 可忽略（仍可用于未开库时的本地模式）  
3. 成功调用：
   - `billing_free=false` → 写入 `billing_events` + 累加 `usage_daily`
   - `billing_free=true` → 只累加 `usage_daily`，不进计费流水  
4. 限流规则仍来自 Token 关联的 `plans`，**免费 Token 照样限流**

## Code map

| Package | Role |
|---------|------|
| `internal/db` | 连接池、Token 查询、计费写入 |
| `internal/auth` | `NewStoreFromDB` / `NewStoreFromConfig` |
| `internal/billing` | `PostgresPublisher` 写入账本 |

## Notes

- 网关热路径不直接查库做鉴权，只读内存缓存，避免拖慢 RPC。  
- `billing_events.event_id` 幂等，重复投递不会双计。  
- 月结 / 发票可后续用 `billing_ledger` 聚合 `billing_events`。  
- 生产请把 DSN 放到环境变量或密钥管理，勿提交真实密码。
