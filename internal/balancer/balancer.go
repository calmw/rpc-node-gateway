package balancer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cisco/rpc-node-gateway/internal/config"
)

type Node struct {
	Name   string
	URL    string
	WSURL  string
	Weight int

	healthy   atomic.Bool
	failCount atomic.Int32
	okCount   atomic.Int32
}

func (n *Node) Healthy() bool {
	return n.healthy.Load()
}

// NodeView 只读快照，供运维/Admin 查询。
type NodeView struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	WSURL   string `json:"ws_url"`
	Weight  int    `json:"weight"`
	Healthy bool   `json:"healthy"`
}

// Pool 单个网络（chain）的上游节点池：节点可增减，网络 ID 保持稳定。
type Pool struct {
	chainID string
	nodes   []*Node

	mu   sync.Mutex
	rr   int
	http *http.Client

	unhealthyThreshold int32
	healthyThreshold   int32
}

type Registry struct {
	mu    sync.RWMutex
	pools map[string]*Pool

	client             *http.Client
	unhealthyThreshold int32
	healthyThreshold   int32
}

func NewRegistry(cfg *config.Config) *Registry {
	client := &http.Client{Timeout: cfg.HealthCheck.Timeout}
	r := &Registry{
		pools:              make(map[string]*Pool, len(cfg.Chains)),
		client:             client,
		unhealthyThreshold: int32(cfg.HealthCheck.UnhealthyThreshold),
		healthyThreshold:   int32(cfg.HealthCheck.HealthyThreshold),
	}
	for id, chain := range cfg.Chains {
		r.pools[id] = newPool(id, chain.Nodes, client, r.unhealthyThreshold, r.healthyThreshold)
	}
	return r
}

func newPool(chainID string, cfgs []config.NodeConfig, client *http.Client, unhealthy, healthy int32) *Pool {
	return &Pool{
		chainID:            chainID,
		nodes:              buildNodes(cfgs),
		http:               client,
		unhealthyThreshold: unhealthy,
		healthyThreshold:   healthy,
	}
}

func buildNodes(cfgs []config.NodeConfig) []*Node {
	nodes := make([]*Node, 0, len(cfgs))
	for i, n := range cfgs {
		name := n.Name
		if name == "" {
			name = fmt.Sprintf("node-%d", i+1)
		}
		w := n.Weight
		if w <= 0 {
			w = 1
		}
		node := &Node{
			Name:   name,
			URL:    n.URL,
			WSURL:  DeriveWSURL(n.URL, n.WSURL),
			Weight: w,
		}
		node.healthy.Store(true)
		nodes = append(nodes, node)
	}
	return nodes
}

func (r *Registry) Pool(chainID string) (*Pool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.pools[chainID]
	return p, ok
}

func (r *Registry) ChainIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.pools))
	for id := range r.pools {
		ids = append(ids, id)
	}
	return ids
}

// ReloadNodes 热更新已有网络的节点列表。
// 只更新 nodes，不新增/删除网络（路由与 path 保持不变）。
func (r *Registry) ReloadNodes(chains map[string]config.Chain) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for id := range chains {
		if _, ok := r.pools[id]; !ok {
			slog.Warn("skip unknown chain on node reload (networks are fixed at startup)", "chain", id)
		}
	}
	for id, pool := range r.pools {
		chain, ok := chains[id]
		if !ok {
			slog.Warn("chain missing in new config, keep existing nodes", "chain", id)
			continue
		}
		if len(chain.Nodes) == 0 {
			return fmt.Errorf("chain %q has no nodes", id)
		}
		pool.ReplaceNodes(chain.Nodes)
		slog.Info("chain nodes reloaded", "chain", id, "nodes", len(chain.Nodes))
	}
	return nil
}

func (r *Registry) Snapshot() map[string][]NodeView {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string][]NodeView, len(r.pools))
	for id, p := range r.pools {
		out[id] = p.Snapshot()
	}
	return out
}

func (r *Registry) StartHealthChecks(ctx context.Context, interval time.Duration) {
	r.mu.RLock()
	pools := make([]*Pool, 0, len(r.pools))
	for _, p := range r.pools {
		pools = append(pools, p)
	}
	r.mu.RUnlock()
	for _, p := range pools {
		pool := p
		go pool.loop(ctx, interval)
	}
}

// ReplaceNodes 替换本网络节点池（扩容/缩容节点，不改 chainID）。
func (p *Pool) ReplaceNodes(cfgs []config.NodeConfig) {
	next := buildNodes(cfgs)
	p.mu.Lock()
	p.nodes = next
	p.rr = 0
	p.mu.Unlock()
}

func (p *Pool) Snapshot() []NodeView {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]NodeView, 0, len(p.nodes))
	for _, n := range p.nodes {
		out = append(out, NodeView{
			Name:    n.Name,
			URL:     n.URL,
			WSURL:   n.WSURL,
			Weight:  n.Weight,
			Healthy: n.Healthy(),
		})
	}
	return out
}

func (p *Pool) Next() (*Node, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	healthy := make([]*Node, 0, len(p.nodes))
	totalWeight := 0
	for _, n := range p.nodes {
		if n.Healthy() {
			healthy = append(healthy, n)
			totalWeight += n.Weight
		}
	}
	if len(healthy) == 0 {
		if len(p.nodes) == 0 {
			return nil, false
		}
		p.rr = (p.rr + 1) % len(p.nodes)
		return p.nodes[p.rr], true
	}

	p.rr = (p.rr + 1) % totalWeight
	cursor := 0
	for _, n := range healthy {
		cursor += n.Weight
		if p.rr < cursor {
			return n, true
		}
	}
	return healthy[0], true
}

func (p *Pool) ReportSuccess(node *Node) {
	node.failCount.Store(0)
	ok := node.okCount.Add(1)
	if !node.Healthy() && ok >= p.healthyThreshold {
		node.healthy.Store(true)
		node.okCount.Store(0)
		slog.Info("upstream recovered", "chain", p.chainID, "name", node.Name, "url", node.URL)
	}
}

func (p *Pool) ReportFailure(node *Node) {
	node.okCount.Store(0)
	fails := node.failCount.Add(1)
	if node.Healthy() && fails >= p.unhealthyThreshold {
		node.healthy.Store(false)
		slog.Warn("upstream marked unhealthy", "chain", p.chainID, "name", node.Name, "url", node.URL, "fails", fails)
	}
}

func (p *Pool) loop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	p.checkAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.checkAll(ctx)
		}
	}
}

func (p *Pool) checkAll(ctx context.Context) {
	p.mu.Lock()
	nodes := append([]*Node(nil), p.nodes...)
	p.mu.Unlock()
	for _, node := range nodes {
		if err := p.probe(ctx, node.URL); err != nil {
			p.ReportFailure(node)
			slog.Debug("health check failed", "chain", p.chainID, "name", node.Name, "url", node.URL, "err", err)
			continue
		}
		p.ReportSuccess(node)
	}
}

func (p *Pool) probe(ctx context.Context, url string) error {
	body := []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	var out struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if len(out.Error) > 0 && string(out.Error) != "null" {
		return fmt.Errorf("json-rpc error")
	}
	return nil
}

// DeriveWSURL 由 HTTP(S) URL 推导 WS(S)；explicit 非空时优先。
func DeriveWSURL(httpURL, explicit string) string {
	if explicit != "" {
		return explicit
	}
	switch {
	case strings.HasPrefix(httpURL, "https://"):
		return "wss://" + strings.TrimPrefix(httpURL, "https://")
	case strings.HasPrefix(httpURL, "http://"):
		return "ws://" + strings.TrimPrefix(httpURL, "http://")
	case strings.HasPrefix(httpURL, "wss://"), strings.HasPrefix(httpURL, "ws://"):
		return httpURL
	default:
		return ""
	}
}

// NextWS 选取带有效 WSURL 的健康节点。
func (p *Pool) NextWS() (*Node, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	candidates := make([]*Node, 0, len(p.nodes))
	totalWeight := 0
	for _, n := range p.nodes {
		if n.WSURL == "" {
			continue
		}
		if n.Healthy() {
			candidates = append(candidates, n)
			totalWeight += n.Weight
		}
	}
	if len(candidates) == 0 {
		for _, n := range p.nodes {
			if n.WSURL != "" {
				candidates = append(candidates, n)
				totalWeight += n.Weight
			}
		}
	}
	if len(candidates) == 0 || totalWeight <= 0 {
		return nil, false
	}
	p.rr = (p.rr + 1) % totalWeight
	cursor := 0
	for _, n := range candidates {
		cursor += n.Weight
		if p.rr < cursor {
			return n, true
		}
	}
	return candidates[0], true
}
