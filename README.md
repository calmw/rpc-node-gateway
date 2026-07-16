# rpc-node-gateway

区块链 JSON-RPC 网关：域名绑定（HTTP）、路径 Token 鉴权、双模式限流、方法黑名单、按链负载均衡，并按成功次数计费（支持免费 Token）。

## 访问方式

```text
http://{绑定域名}/{token}/{chain}
```

示例：

```bash
curl -s http://127.0.0.1:8080/demo-free-token/eth \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
```

Token **只放在 URL 路径**，不使用 Header。当前仅 HTTP，不启用 HTTPS。

## 能力

| 能力 | 说明 |
|------|------|
| 域名绑定 | `server.domains` 校验 `Host`（忽略端口）；空列表则不校验 |
| Token 鉴权 | 路径 `/{token}/{chain}` |
| 方法过滤 | 按套餐 `denied_methods`（支持 batch） |
| 限流模式 1 | **token + IP**（`token_ip_rate_limit_*`），详见 [docs/rate-limiting.md](docs/rate-limiting.md) |
| 限流模式 2 | **token 全局**（`token_rate_limit_*`，不分 IP），详见 [docs/rate-limiting.md](docs/rate-limiting.md) |
| 日配额 | 按成功次数累计，所有 token 都受限流/配额约束 |
| 计费 | 成功调用计费；`billing_free: true` 不计费，**但仍限流** |
| 数据持久化 | 可选 PostgreSQL：用户 / Token / 套餐 / 计费账本，见 [docs/database.md](docs/database.md) |
| 统计 | 全局 + 按 Token / 链；含 WS 推送；Admin JWT，见 [docs/statistics.md](docs/statistics.md) |
| WebSocket | 同路径 Upgrade；入站 RPC + 推送与 HTTP 按次同权，见 [docs/websocket.md](docs/websocket.md) |
| 节点扩容 | 网络（主网/测试网）固定；同网络下 `nodes` 可 1→N，支持热更新，见 [docs/nodes.md](docs/nodes.md) |
| 负载均衡 | 按网络加权轮询 + 健康检查 + 失败重试 |

## 快速开始

```bash
go run ./cmd/gateway -config configs/config.yaml
```

演示 Token：

- `demo-free-token`：免费（`billing_free: true`），仍限流
- `demo-pro-token`：计费

## 配置要点

见 `configs/config.yaml`：

```yaml
server:
  domains: ["rpc.example.com", "localhost", "127.0.0.1"]  # 绑定域名

plans:
  free:
    token_ip_rate_limit_per_second: 3   # token+IP
    token_rate_limit_per_second: 5      # token 全局
    ...

tokens:
  - key: demo-free-token
    billing_free: true   # 不计费，仍限流
```

生产环境建议开启 PostgreSQL 存 Token 与账本，并把 DNS/反代把业务域名指到本网关。

## 文档

- [Rate Limiting](docs/rate-limiting.md) — 双模式限流、日配额、配置与拒绝响应
- [Database](docs/database.md) — PostgreSQL schema、迁移、Token 加载与计费落库
- [Statistics](docs/statistics.md) — 全局 / Token 请求统计与 Admin API
- [WebSocket](docs/websocket.md) — WS 代理与按次计量（与 HTTP 同权）
- [Nodes](docs/nodes.md) — 网络固定、节点可扩容与热更新

## 计费约定

- 鉴权失败 / 方法禁止 / 限流：**不计费**
- 上游成功且 JSON-RPC 无 `error`：计入日配额（Redis 热计数；开库时再写 `usage_daily`）
- `billing_free: false`：进入计费流水（日志 / Redis Stream / `billing_events`）
- `billing_free: true`：只记 `success_free` + 用量，不进计费流水
- 限流对 **所有** token 生效，与是否免费无关
