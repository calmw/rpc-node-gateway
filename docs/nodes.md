# Nodes & Networks

网关把 **网络（chain）** 和 **节点（node）** 分开：

| 概念 | 含义 | 后期变化 |
|------|------|----------|
| Network / chain | 如主网 `eth`、测试网 `sepolia` | **一般不变**；决定 URL 路径 `/{token}/{chain}` |
| Node | 该网络下的 RPC 上游 | **可扩缩**：1 → 2 → N |

因此「主网从 1 个节点扩到 2 个」只需在对应 `chains.<id>.nodes` 下追加条目，**不必新增网络、不必改客户端 path**。

## Config model

```yaml
chains:
  eth:                    # 主网（固定）
    name: "Ethereum Mainnet"
    nodes:                # 节点池（可扩）
      - name: "publicnode-1"
        url: "https://ethereum.publicnode.com"
        ws_url: "wss://ethereum.publicnode.com"
        weight: 1
      # 扩容：追加第二个节点
      - name: "ankr-1"
        url: "https://rpc.ankr.com/eth"
        weight: 1

  sepolia:                # 测试网（固定）
    name: "Ethereum Sepolia"
    nodes:
      - name: "publicnode-sepolia-1"
        url: "https://ethereum-sepolia.publicnode.com"
        weight: 1
```

访问：

- 主网：`http(s)://{domain}/{token}/eth` / `ws://.../eth`
- 测试网：`.../{token}/sepolia`

同一网络内多节点：

- **加权轮询**（`weight`）
- **健康检查**失败自动摘除，恢复后再加回
- HTTP / WS 各自选可用节点

## Hot reload（只更新节点）

改完 `configs/config.yaml` 的 `nodes` 后，任选其一：

```bash
# 1) 信号热更新
kill -HUP $(pgrep -f 'gateway')

# 2) Admin API（需 JWT）
curl -X POST http://127.0.0.1:8080/admin/upstreams/reload \
  -H "Authorization: Bearer $ADMIN_JWT"
```

查看当前节点与健康状态：

```bash
curl -s http://127.0.0.1:8080/admin/upstreams \
  -H "Authorization: Bearer $ADMIN_JWT"
```

热更新规则：

- ✅ 更新已有网络的 `nodes`（增删改 URL/权重）
- ⚠️ 配置里新出现的网络 ID **不会**自动挂路由（网络在启动时固定）
- ⚠️ 配置里删掉某个网络时，**保留**内存中原节点（避免误删）；要下线网络需重启并改路由

## Ops tip

给每个节点起稳定的 `name`（如 `publicnode-1`），扩容、排障、日志都更好认。
