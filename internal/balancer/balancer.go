package balancer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cisco/rpc-node-gateway/internal/config"
)

type Node struct {
	URL    string
	Weight int

	healthy   atomic.Bool
	failCount atomic.Int32
	okCount   atomic.Int32
}

func (n *Node) Healthy() bool {
	return n.healthy.Load()
}

// Pool 单个链的上游节点池：加权轮询 + 健康检查。
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
	pools map[string]*Pool
}

func NewRegistry(cfg *config.Config) *Registry {
	r := &Registry{pools: make(map[string]*Pool, len(cfg.Chains))}
	client := &http.Client{Timeout: cfg.HealthCheck.Timeout}
	for id, chain := range cfg.Chains {
		nodes := make([]*Node, 0, len(chain.Nodes))
		for _, n := range chain.Nodes {
			node := &Node{URL: n.URL, Weight: n.Weight}
			node.healthy.Store(true)
			nodes = append(nodes, node)
		}
		r.pools[id] = &Pool{
			chainID:            id,
			nodes:              nodes,
			http:               client,
			unhealthyThreshold: int32(cfg.HealthCheck.UnhealthyThreshold),
			healthyThreshold:   int32(cfg.HealthCheck.HealthyThreshold),
		}
	}
	return r
}

func (r *Registry) Pool(chainID string) (*Pool, bool) {
	p, ok := r.pools[chainID]
	return p, ok
}

func (r *Registry) StartHealthChecks(ctx context.Context, interval time.Duration) {
	for _, p := range r.pools {
		pool := p
		go pool.loop(ctx, interval)
	}
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
		slog.Info("upstream recovered", "chain", p.chainID, "url", node.URL)
	}
}

func (p *Pool) ReportFailure(node *Node) {
	node.okCount.Store(0)
	fails := node.failCount.Add(1)
	if node.Healthy() && fails >= p.unhealthyThreshold {
		node.healthy.Store(false)
		slog.Warn("upstream marked unhealthy", "chain", p.chainID, "url", node.URL, "fails", fails)
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
	for _, node := range p.nodes {
		if err := p.probe(ctx, node.URL); err != nil {
			p.ReportFailure(node)
			slog.Debug("health check failed", "chain", p.chainID, "url", node.URL, "err", err)
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
