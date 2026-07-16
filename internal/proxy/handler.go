package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/cisco/rpc-node-gateway/internal/balancer"
	"github.com/cisco/rpc-node-gateway/internal/billing"
	"github.com/cisco/rpc-node-gateway/internal/gatewayctx"
	"github.com/cisco/rpc-node-gateway/internal/httputil"
	"github.com/cisco/rpc-node-gateway/internal/jsonrpc"
	"github.com/cisco/rpc-node-gateway/internal/model"
	"github.com/cisco/rpc-node-gateway/internal/ratelimit"
	"github.com/cisco/rpc-node-gateway/internal/stats"
	"github.com/google/uuid"
)

type Handler struct {
	Registry *balancer.Registry
	Limiter  *ratelimit.Limiter
	Billing  billing.Publisher
	Stats    *stats.Collector
	Client   *http.Client
}

func NewHandler(reg *balancer.Registry, lim *ratelimit.Limiter, bill billing.Publisher, st *stats.Collector) *Handler {
	return &Handler{
		Registry: reg,
		Limiter:  lim,
		Billing:  bill,
		Stats:    st,
		Client: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        200,
				MaxIdleConnsPerHost: 50,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, -32600, "only POST is allowed")
		return
	}

	token := gatewayctx.Token(r.Context())
	chainID := gatewayctx.ChainID(r.Context())
	methods := gatewayctx.Methods(r.Context())
	clientIP := httputil.ClientIP(r)
	if token == nil || chainID == "" {
		writeJSONError(w, http.StatusUnauthorized, -32001, "unauthorized")
		return
	}
	meta := stats.TokenMeta{
		Key:         token.Key,
		Name:        token.Name,
		Plan:        token.Plan,
		BillingFree: token.BillingFree,
		Enabled:     token.Enabled,
	}

	pool, ok := h.Registry.Pool(chainID)
	if !ok {
		writeJSONError(w, http.StatusNotFound, -32002, "unknown chain")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20)) // 2MB
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, -32700, "failed to read body")
		return
	}

	node, ok := pool.Next()
	if !ok {
		if h.Stats != nil {
			h.Stats.RecordProxy(meta, chainID, 0, 0, true, false)
		}
		writeJSONError(w, http.StatusBadGateway, -32003, "no upstream available")
		return
	}

	start := time.Now()
	respBody, status, err := h.forward(r.Context(), node.URL, body, r.Header)
	latency := time.Since(start)

	if err != nil {
		pool.ReportFailure(node)
		slog.Warn("upstream request failed", "chain", chainID, "upstream", node.URL, "err", err)
		if alt, ok := pool.Next(); ok && alt.URL != node.URL {
			respBody, status, err = h.forward(r.Context(), alt.URL, body, r.Header)
			latency = time.Since(start)
			if err == nil {
				node = alt
			}
		}
	}

	if err != nil {
		if h.Stats != nil {
			h.Stats.RecordProxy(meta, chainID, 0, 0, true, false)
		}
		writeJSONError(w, http.StatusBadGateway, -32004, "upstream unavailable")
		return
	}

	if status >= 500 {
		pool.ReportFailure(node)
	} else {
		pool.ReportSuccess(node)
	}

	successN, rpcErrN := 0, 0
	if status >= 200 && status < 300 {
		successN, rpcErrN = jsonrpc.CountResults(respBody)
	} else if status >= 500 {
		if h.Stats != nil {
			h.Stats.RecordProxy(meta, chainID, 0, 0, true, !token.BillingFree)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(respBody)
		return
	}

	billable := !token.BillingFree
	if h.Stats != nil {
		h.Stats.RecordProxy(meta, chainID, successN, rpcErrN, false, billable)
	}

	if successN > 0 {
		h.Limiter.IncrSuccess(r.Context(), token.Key, successN)

		amount := 0
		if billable && token.PricePerSuccessCents > 0 {
			amount = token.PricePerSuccessCents * successN
		}
		ev := model.BillingEvent{
			EventID:     uuid.NewString(),
			TokenKey:    token.Key,
			TokenName:   token.Name,
			UserID:      token.UserID,
			Plan:        token.Plan,
			ChainID:     chainID,
			Methods:     methods,
			SuccessN:    successN,
			Billable:    billable,
			AmountCents: amount,
			Transport:   "http",
			EventKind:   "rpc",
			Upstream:    node.URL,
			LatencyMs:   latency.Milliseconds(),
			ClientIP:    clientIP,
			At:          time.Now().UTC(),
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := h.Billing.Publish(ctx, ev); err != nil {
				slog.Error("publish billing event failed", "err", err)
			}
		}()
		if !billable {
			slog.Info("success_free",
				"token", token.Key,
				"chain", chainID,
				"success_n", successN,
				"ip", clientIP,
			)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(respBody)
}

func (h *Handler) forward(ctx context.Context, upstream string, body []byte, hdr http.Header) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if ua := hdr.Get("User-Agent"); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, 0, err
	}
	return respBody, resp.StatusCode, nil
}

func writeJSONError(w http.ResponseWriter, httpStatus int, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      nil,
		"error": map[string]interface{}{
			"code":    code,
			"message": msg,
		},
	})
}
