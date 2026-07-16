# WebSocket

网关在同一路径支持 WebSocket Upgrade，计量口径与 HTTP **按成功次数同权**，避免挂长连接白嫖。

## Endpoint

```text
ws://{domain}/{token}/{chain}
```

与 HTTP `POST /{token}/{chain}` 相同鉴权（路径 Token）与域名绑定。

## Billing fairness

| 事件 | 是否计入 success_calls | 说明 |
|------|------------------------|------|
| 建连 / 断开 / ping | 否 | 不按连接收费 |
| 客户端发起的 RPC（含 `eth_subscribe`）成功 | **是（1 条计 1）** | 与 HTTP 相同 |
| 上游 `eth_subscription` 推送 | **是（默认 1 条计 1）** | 与 HTTP 请求同权，防白嫖 |
| RPC error 响应 | 不计成功，记 `rpc_errors` | 与 HTTP 相同 |
| 限流 / 方法黑名单 | 拒绝，不计成功 | 与 HTTP 相同 |

`notification_bill_units` 可调推送折算权重；要与 HTTP 完全同权请保持 `1`。

免费 Token（`billing_free`）仍统计并限流，只是不进计费流水。

## Config

```yaml
websocket:
  enabled: true
  max_connections_per_token: 5
  max_subscriptions_per_connection: 20
  notification_bill_units: 1

chains:
  eth:
    nodes:
      - url: "https://ethereum.publicnode.com"
        ws_url: "wss://ethereum.publicnode.com"  # 可省略，自动 https→wss
```

## Stats fields

| Field | Meaning |
|-------|---------|
| `ws_connections` | 建连次数 |
| `ws_notifications` | 推送原始条数 |
| `success_calls` | HTTP/WS 入站成功 + 推送折算次数 |
| `billable_calls` / `free_calls` | 计费/免费用量（含推送） |

计费事件增加：

- `transport`: `http` \| `ws`
- `event_kind`: `rpc` \| `ws_notification`

## Implementation

- `internal/wsproxy` — 双向代理、入站限流/方法过滤、出站推送识别
- 推送识别：无 `id` 且 `method` 为 subscription 通知（如 `eth_subscription`）
