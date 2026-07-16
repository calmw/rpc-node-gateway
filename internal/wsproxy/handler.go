package wsproxy

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cisco/rpc-node-gateway/internal/balancer"
	"github.com/cisco/rpc-node-gateway/internal/billing"
	"github.com/cisco/rpc-node-gateway/internal/config"
	"github.com/cisco/rpc-node-gateway/internal/filter"
	"github.com/cisco/rpc-node-gateway/internal/gatewayctx"
	"github.com/cisco/rpc-node-gateway/internal/httputil"
	"github.com/cisco/rpc-node-gateway/internal/jsonrpc"
	"github.com/cisco/rpc-node-gateway/internal/model"
	"github.com/cisco/rpc-node-gateway/internal/ratelimit"
	"github.com/cisco/rpc-node-gateway/internal/stats"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type ConnGuard struct {
	mu    sync.Mutex
	count map[string]int
	max   int
}

func NewConnGuard(max int) *ConnGuard {
	return &ConnGuard{count: make(map[string]int), max: max}
}

func (g *ConnGuard) Acquire(token string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.max > 0 && g.count[token] >= g.max {
		return false
	}
	g.count[token]++
	return true
}

func (g *ConnGuard) Release(token string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.count[token] > 0 {
		g.count[token]--
	}
	if g.count[token] == 0 {
		delete(g.count, token)
	}
}

type Handler struct {
	Registry *balancer.Registry
	Limiter  *ratelimit.Limiter
	Billing  billing.Publisher
	Stats    *stats.Collector
	Cfg      config.WebSocketConfig
	Conns    *ConnGuard
}

func NewHandler(reg *balancer.Registry, lim *ratelimit.Limiter, bill billing.Publisher, st *stats.Collector, cfg config.WebSocketConfig) *Handler {
	return &Handler{
		Registry: reg,
		Limiter:  lim,
		Billing:  bill,
		Stats:    st,
		Cfg:      cfg,
		Conns:    NewConnGuard(cfg.MaxConnectionsPerToken),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := gatewayctx.Token(r.Context())
	chainID := gatewayctx.ChainID(r.Context())
	if token == nil || chainID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	meta := stats.TokenMeta{
		Key: token.Key, Name: token.Name, Plan: token.Plan,
		BillingFree: token.BillingFree, Enabled: token.Enabled,
	}
	clientIP := httputil.ClientIP(r)

	if !h.Conns.Acquire(token.Key) {
		http.Error(w, "too many websocket connections", http.StatusTooManyRequests)
		return
	}
	defer h.Conns.Release(token.Key)

	pool, ok := h.Registry.Pool(chainID)
	if !ok {
		http.Error(w, "unknown chain", http.StatusNotFound)
		return
	}
	node, ok := pool.NextWS()
	if !ok || node.WSURL == "" {
		http.Error(w, "no websocket upstream", http.StatusBadGateway)
		return
	}

	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws upgrade failed", "err", err)
		return
	}
	defer clientConn.Close()

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	upstream, _, err := dialer.Dial(node.WSURL, nil)
	if err != nil {
		slog.Warn("ws dial upstream failed", "url", node.WSURL, "err", err)
		_ = clientConn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "upstream unavailable"))
		return
	}
	defer upstream.Close()

	if h.Stats != nil {
		h.Stats.RecordWSConnect(meta, chainID)
	}

	sess := &session{
		h:          h,
		token:      token,
		meta:       meta,
		chainID:    chainID,
		clientIP:   clientIP,
		upstream:   node.WSURL,
		client:     clientConn,
		server:     upstream,
		pending:    make(map[string]pendingReq),
		subs:       make(map[string]string), // subscriptionID -> kind
		billUnits:  h.Cfg.NotificationBillUnits,
		maxSubs:    h.Cfg.MaxSubscriptionsPerConn,
	}

	errCh := make(chan error, 2)
	go func() { errCh <- sess.pumpClientToUpstream() }()
	go func() { errCh <- sess.pumpUpstreamToClient() }()
	<-errCh
}

type pendingReq struct {
	method  string
	subKind string
}

type session struct {
	h        *Handler
	token    *model.Token
	meta     stats.TokenMeta
	chainID  string
	clientIP string
	upstream string
	client   *websocket.Conn
	server   *websocket.Conn

	mu      sync.Mutex
	pending map[string]pendingReq
	subs    map[string]string
	subN    int

	billUnits int
	maxSubs   int
	closed    atomic.Bool
}

func (s *session) pumpClientToUpstream() error {
	for {
		_, data, err := s.client.ReadMessage()
		if err != nil {
			s.closeBoth()
			return err
		}
		if !s.handleInbound(data) {
			continue // 已拒绝，不转发
		}
		if err := s.server.WriteMessage(websocket.TextMessage, data); err != nil {
			s.closeBoth()
			return err
		}
	}
}

func (s *session) pumpUpstreamToClient() error {
	for {
		_, data, err := s.server.ReadMessage()
		if err != nil {
			s.closeBoth()
			return err
		}
		s.handleOutbound(data)
		if err := s.client.WriteMessage(websocket.TextMessage, data); err != nil {
			s.closeBoth()
			return err
		}
	}
}

func (s *session) closeBoth() {
	if s.closed.Swap(true) {
		return
	}
	_ = s.client.Close()
	_ = s.server.Close()
}

func (s *session) handleInbound(data []byte) bool {
	// 限流：每条入站消息一次（与 HTTP 请求同权）
	res := s.h.Limiter.Allow(context.Background(), s.token, s.chainID, s.clientIP)
	if !res.Allowed {
		if s.h.Stats != nil {
			s.h.Stats.IncRateLimited(s.meta)
		}
		_ = s.client.WriteMessage(websocket.TextMessage, []byte(
			`{"jsonrpc":"2.0","id":null,"error":{"code":-32005,"message":"rate limited: `+res.Reason+`"}}`,
		))
		return false
	}

	kind := jsonrpc.Classify(data)
	if kind == jsonrpc.KindRequest {
		methods, err := jsonrpc.ParseMethods(data)
		if err != nil {
			_ = s.client.WriteMessage(websocket.TextMessage, []byte(
				`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"invalid request"}}`,
			))
			return false
		}
		if err := filter.CheckMethods(s.token, methods); err != nil {
			if s.h.Stats != nil {
				s.h.Stats.IncMethodDenied(s.meta)
			}
			_ = s.client.WriteMessage(websocket.TextMessage, []byte(
				`{"jsonrpc":"2.0","id":null,"error":{"code":-32601,"message":"`+err.Error()+`"}}`,
			))
			return false
		}
		// 单条请求登记 pending（batch 按整体成功条数在响应侧用 CountResults）
		if len(methods) == 1 {
			id, method, subKind, err := jsonrpc.ParseRequestMeta(data)
			if err == nil && id != "" {
				if method == "eth_subscribe" && s.maxSubs > 0 {
					s.mu.Lock()
					over := s.subN >= s.maxSubs
					s.mu.Unlock()
					if over {
						_ = s.client.WriteMessage(websocket.TextMessage, []byte(
							`{"jsonrpc":"2.0","id":null,"error":{"code":-32006,"message":"too many subscriptions"}}`,
						))
						return false
					}
				}
				s.mu.Lock()
				s.pending[id] = pendingReq{method: method, subKind: subKind}
				s.mu.Unlock()
			}
		}
	}
	return true
}

func (s *session) handleOutbound(data []byte) {
	method, subID, isNotify := jsonrpc.ParseNotification(data)
	if isNotify {
		units := s.billUnits
		if units <= 0 {
			units = 1
		}
		subKind := method
		if subID != "" {
			s.mu.Lock()
			if k, exists := s.subs[subID]; exists && k != "" {
				subKind = k
			}
			s.mu.Unlock()
		}
		billable := !s.token.BillingFree
		if s.h.Stats != nil {
			s.h.Stats.RecordWSNotification(s.meta, s.chainID, 1, units, billable)
		}
		s.h.Limiter.IncrSuccess(context.Background(), s.token.Key, units)
		s.publish(units, billable, []string{subKind}, "ws_notification")
		return
	}

	successN, rpcErrN := jsonrpc.CountResults(data)
	id, okSucc, result, err := jsonrpc.ParseResponseMeta(data)
	if err == nil && id != "" {
		s.mu.Lock()
		pr, found := s.pending[id]
		delete(s.pending, id)
		s.mu.Unlock()
		if found {
			if successN == 0 && rpcErrN == 0 {
				if okSucc {
					successN = 1
				} else {
					rpcErrN = 1
				}
			}
			if okSucc && pr.method == "eth_subscribe" {
				sid := trimQuotes(result)
				if sid != "" {
					s.mu.Lock()
					s.subs[sid] = pr.subKind
					s.subN++
					s.mu.Unlock()
				}
			}
			if okSucc && pr.method == "eth_unsubscribe" {
				s.mu.Lock()
				if s.subN > 0 {
					s.subN--
				}
				s.mu.Unlock()
			}
		}
	}

	if successN == 0 && rpcErrN == 0 {
		return
	}
	billable := !s.token.BillingFree
	if s.h.Stats != nil {
		s.h.Stats.RecordProxy(s.meta, s.chainID, successN, rpcErrN, false, billable)
	}
	if successN > 0 {
		s.h.Limiter.IncrSuccess(context.Background(), s.token.Key, successN)
		s.publish(successN, billable, nil, "rpc")
	}
}

func (s *session) publish(successN int, billable bool, methods []string, kind string) {
	if successN <= 0 {
		return
	}
	amount := 0
	if billable && s.token.PricePerSuccessCents > 0 {
		amount = s.token.PricePerSuccessCents * successN
	}
	ev := model.BillingEvent{
		EventID:     uuid.NewString(),
		TokenKey:    s.token.Key,
		TokenName:   s.token.Name,
		UserID:      s.token.UserID,
		Plan:        s.token.Plan,
		ChainID:     s.chainID,
		Methods:     methods,
		SuccessN:    successN,
		Billable:    billable,
		AmountCents: amount,
		Transport:   "ws",
		EventKind:   kind,
		Upstream:    s.upstream,
		ClientIP:    s.clientIP,
		At:          time.Now().UTC(),
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.h.Billing.Publish(ctx, ev); err != nil {
			slog.Error("ws billing publish failed", "err", err)
		}
	}()
}

func trimQuotes(s string) string {
	return strings.Trim(s, `"`)
}
