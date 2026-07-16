package stats

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics 一组计数指标。
type Metrics struct {
	Requests         int64 `json:"requests"`
	Proxied          int64 `json:"proxied"`
	SuccessCalls     int64 `json:"success_calls"` // HTTP/WS 入站成功 + WS 推送折算（同权）
	RPCErrors        int64 `json:"rpc_errors"`
	UpstreamErrors   int64 `json:"upstream_errors"`
	AuthFailed       int64 `json:"auth_failed"`
	MethodDenied     int64 `json:"method_denied"`
	RateLimited      int64 `json:"rate_limited"`
	DomainRejected   int64 `json:"domain_rejected"`
	BillableCalls    int64 `json:"billable_calls"`
	FreeCalls        int64 `json:"free_calls"`
	WSConnections   int64 `json:"ws_connections"`  // 历史建连次数
	WSNotifications int64 `json:"ws_notifications"` // 推送条数（未乘权重前）
}

type atomicMetrics struct {
	Requests        atomic.Int64
	Proxied         atomic.Int64
	SuccessCalls    atomic.Int64
	RPCErrors       atomic.Int64
	UpstreamErrors  atomic.Int64
	AuthFailed      atomic.Int64
	MethodDenied    atomic.Int64
	RateLimited     atomic.Int64
	DomainRejected  atomic.Int64
	BillableCalls   atomic.Int64
	FreeCalls       atomic.Int64
	WSConnections   atomic.Int64
	WSNotifications atomic.Int64
	LastUnixNano    atomic.Int64
}

func (m *atomicMetrics) snapshot() Metrics {
	return Metrics{
		Requests:        m.Requests.Load(),
		Proxied:         m.Proxied.Load(),
		SuccessCalls:    m.SuccessCalls.Load(),
		RPCErrors:       m.RPCErrors.Load(),
		UpstreamErrors:  m.UpstreamErrors.Load(),
		AuthFailed:      m.AuthFailed.Load(),
		MethodDenied:    m.MethodDenied.Load(),
		RateLimited:     m.RateLimited.Load(),
		DomainRejected:  m.DomainRejected.Load(),
		BillableCalls:   m.BillableCalls.Load(),
		FreeCalls:       m.FreeCalls.Load(),
		WSConnections:   m.WSConnections.Load(),
		WSNotifications: m.WSNotifications.Load(),
	}
}

func (m *atomicMetrics) touch() {
	m.LastUnixNano.Store(time.Now().UnixNano())
}

func (m *atomicMetrics) lastAt() *time.Time {
	n := m.LastUnixNano.Load()
	if n == 0 {
		return nil
	}
	t := time.Unix(0, n).UTC()
	return &t
}

// TokenSnapshot 单个 token 的统计视图。
type TokenSnapshot struct {
	TokenKey    string     `json:"token_key"`
	TokenName   string     `json:"token_name,omitempty"`
	Plan        string     `json:"plan,omitempty"`
	BillingFree bool       `json:"billing_free"`
	Enabled     bool       `json:"enabled"`
	LastRequest *time.Time `json:"last_request_at,omitempty"`
	Metrics     Metrics    `json:"metrics"`
	ByChain     map[string]Metrics `json:"by_chain,omitempty"`
}

// Overview 总览：全局 + 各 token。
type Overview struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Total       Metrics         `json:"total"`
	TokenCount  int             `json:"token_count"`
	Tokens      []TokenSnapshot `json:"tokens"`
}

type tokenState struct {
	meta   TokenMeta
	metrics atomicMetrics
	chains sync.Map // chainID -> *atomicMetrics
}

// TokenMeta 来自鉴权仓库的元信息。
type TokenMeta struct {
	Key         string
	Name        string
	Plan        string
	BillingFree bool
	Enabled     bool
}

// Collector 进程内统计（多实例时各看各的；后续可接 Redis 聚合）。
type Collector struct {
	global atomicMetrics
	mu     sync.RWMutex
	tokens map[string]*tokenState
}

func NewCollector() *Collector {
	return &Collector{tokens: make(map[string]*tokenState)}
}

func (c *Collector) ensureToken(meta TokenMeta) *tokenState {
	c.mu.RLock()
	st, ok := c.tokens[meta.Key]
	c.mu.RUnlock()
	if ok {
		// 刷新元信息
		c.mu.Lock()
		st.meta = meta
		c.mu.Unlock()
		return st
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if st, ok = c.tokens[meta.Key]; ok {
		st.meta = meta
		return st
	}
	st = &tokenState{meta: meta}
	c.tokens[meta.Key] = st
	return st
}

func (c *Collector) RegisterTokens(list []TokenMeta) {
	for _, m := range list {
		c.ensureToken(m)
	}
}

func (c *Collector) IncDomainRejected() {
	c.global.DomainRejected.Add(1)
	c.global.touch()
}

func (c *Collector) IncAuthFailed() {
	c.global.AuthFailed.Add(1)
	c.global.Requests.Add(1)
	c.global.touch()
}

func (c *Collector) IncMethodDenied(meta TokenMeta) {
	c.global.MethodDenied.Add(1)
	c.global.Requests.Add(1)
	c.global.touch()
	st := c.ensureToken(meta)
	st.metrics.MethodDenied.Add(1)
	st.metrics.Requests.Add(1)
	st.metrics.touch()
}

func (c *Collector) IncRateLimited(meta TokenMeta) {
	c.global.RateLimited.Add(1)
	c.global.Requests.Add(1)
	c.global.touch()
	st := c.ensureToken(meta)
	st.metrics.RateLimited.Add(1)
	st.metrics.Requests.Add(1)
	st.metrics.touch()
}

// RecordProxy 记录一次已转发（或尝试转发）的结果。
func (c *Collector) RecordProxy(meta TokenMeta, chainID string, successCalls, rpcErrors int, upstreamErr bool, billable bool) {
	c.global.Requests.Add(1)
	c.global.Proxied.Add(1)
	c.global.touch()

	st := c.ensureToken(meta)
	st.metrics.Requests.Add(1)
	st.metrics.Proxied.Add(1)
	st.metrics.touch()

	cm := c.chainMetrics(st, chainID)
	cm.Requests.Add(1)
	cm.Proxied.Add(1)
	cm.touch()

	if upstreamErr {
		c.global.UpstreamErrors.Add(1)
		st.metrics.UpstreamErrors.Add(1)
		cm.UpstreamErrors.Add(1)
		return
	}
	if successCalls > 0 {
		c.global.SuccessCalls.Add(int64(successCalls))
		st.metrics.SuccessCalls.Add(int64(successCalls))
		cm.SuccessCalls.Add(int64(successCalls))
		if billable {
			c.global.BillableCalls.Add(int64(successCalls))
			st.metrics.BillableCalls.Add(int64(successCalls))
			cm.BillableCalls.Add(int64(successCalls))
		} else {
			c.global.FreeCalls.Add(int64(successCalls))
			st.metrics.FreeCalls.Add(int64(successCalls))
			cm.FreeCalls.Add(int64(successCalls))
		}
	}
	if rpcErrors > 0 {
		c.global.RPCErrors.Add(int64(rpcErrors))
		st.metrics.RPCErrors.Add(int64(rpcErrors))
		cm.RPCErrors.Add(int64(rpcErrors))
	}
}

// RecordWSConnect 记录一次 WS 建连。
func (c *Collector) RecordWSConnect(meta TokenMeta, chainID string) {
	c.global.WSConnections.Add(1)
	c.global.touch()
	st := c.ensureToken(meta)
	st.metrics.WSConnections.Add(1)
	st.metrics.touch()
	cm := c.chainMetrics(st, chainID)
	cm.WSConnections.Add(1)
	cm.touch()
}

// RecordWSNotification 记录推送：条数进 ws_notifications，折算单位进 success_calls（与 HTTP 同权）。
func (c *Collector) RecordWSNotification(meta TokenMeta, chainID string, rawCount, billUnits int, billable bool) {
	if rawCount <= 0 {
		return
	}
	if billUnits <= 0 {
		billUnits = rawCount
	}
	c.global.WSNotifications.Add(int64(rawCount))
	c.global.SuccessCalls.Add(int64(billUnits))
	c.global.touch()

	st := c.ensureToken(meta)
	st.metrics.WSNotifications.Add(int64(rawCount))
	st.metrics.SuccessCalls.Add(int64(billUnits))
	st.metrics.touch()

	cm := c.chainMetrics(st, chainID)
	cm.WSNotifications.Add(int64(rawCount))
	cm.SuccessCalls.Add(int64(billUnits))
	cm.touch()

	if billable {
		c.global.BillableCalls.Add(int64(billUnits))
		st.metrics.BillableCalls.Add(int64(billUnits))
		cm.BillableCalls.Add(int64(billUnits))
	} else {
		c.global.FreeCalls.Add(int64(billUnits))
		st.metrics.FreeCalls.Add(int64(billUnits))
		cm.FreeCalls.Add(int64(billUnits))
	}
}

func (c *Collector) chainMetrics(st *tokenState, chainID string) *atomicMetrics {
	if chainID == "" {
		chainID = "_"
	}
	if v, ok := st.chains.Load(chainID); ok {
		return v.(*atomicMetrics)
	}
	m := &atomicMetrics{}
	actual, _ := st.chains.LoadOrStore(chainID, m)
	return actual.(*atomicMetrics)
}

func (c *Collector) Snapshot(known []TokenMeta) Overview {
	for _, m := range known {
		c.ensureToken(m)
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	tokens := make([]TokenSnapshot, 0, len(c.tokens))
	for _, st := range c.tokens {
		snap := TokenSnapshot{
			TokenKey:    st.meta.Key,
			TokenName:   st.meta.Name,
			Plan:        st.meta.Plan,
			BillingFree: st.meta.BillingFree,
			Enabled:     st.meta.Enabled,
			LastRequest: st.metrics.lastAt(),
			Metrics:     st.metrics.snapshot(),
			ByChain:     map[string]Metrics{},
		}
		st.chains.Range(func(k, v any) bool {
			snap.ByChain[k.(string)] = v.(*atomicMetrics).snapshot()
			return true
		})
		if len(snap.ByChain) == 0 {
			snap.ByChain = nil
		}
		tokens = append(tokens, snap)
	}
	sort.Slice(tokens, func(i, j int) bool {
		return tokens[i].TokenKey < tokens[j].TokenKey
	})

	return Overview{
		GeneratedAt: time.Now().UTC(),
		Total:       c.global.snapshot(),
		TokenCount:  len(tokens),
		Tokens:      tokens,
	}
}

func (c *Collector) TokenSnapshot(key string) (TokenSnapshot, bool) {
	c.mu.RLock()
	st, ok := c.tokens[key]
	c.mu.RUnlock()
	if !ok {
		return TokenSnapshot{}, false
	}
	snap := TokenSnapshot{
		TokenKey:    st.meta.Key,
		TokenName:   st.meta.Name,
		Plan:        st.meta.Plan,
		BillingFree: st.meta.BillingFree,
		Enabled:     st.meta.Enabled,
		LastRequest: st.metrics.lastAt(),
		Metrics:     st.metrics.snapshot(),
		ByChain:     map[string]Metrics{},
	}
	st.chains.Range(func(k, v any) bool {
		snap.ByChain[k.(string)] = v.(*atomicMetrics).snapshot()
		return true
	})
	if len(snap.ByChain) == 0 {
		snap.ByChain = nil
	}
	return snap, true
}
