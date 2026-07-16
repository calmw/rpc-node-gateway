package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cisco/rpc-node-gateway/internal/auth"
	"github.com/cisco/rpc-node-gateway/internal/config"
	"github.com/cisco/rpc-node-gateway/internal/filter"
	"github.com/cisco/rpc-node-gateway/internal/gatewayctx"
	"github.com/cisco/rpc-node-gateway/internal/httputil"
	"github.com/cisco/rpc-node-gateway/internal/jsonrpc"
	"github.com/cisco/rpc-node-gateway/internal/model"
	"github.com/cisco/rpc-node-gateway/internal/ratelimit"
	"github.com/cisco/rpc-node-gateway/internal/stats"
	"github.com/go-chi/chi/v5"
)

// Domain 校验 Host 是否在绑定域名列表中（仅 HTTP，不做 TLS）。
type Domain struct {
	Domains []string
	Stats   *stats.Collector
}

func (d Domain) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !config.HostAllowed(d.Domains, r.Host) {
			slog.Warn("domain_rejected", "host", r.Host)
			if d.Stats != nil {
				d.Stats.IncDomainRejected()
			}
			writeErr(w, http.StatusForbidden, -32010, "host not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// PathAuth 从路径 /{token}/... 取 token，不再使用 Header。
type PathAuth struct {
	Tokens *auth.Store
	Stats  *stats.Collector
}

func (a PathAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(chi.URLParam(r, "token"))
		if key == "" {
			if a.Stats != nil {
				a.Stats.IncAuthFailed()
			}
			writeErr(w, http.StatusUnauthorized, -32001, "missing api token in path")
			return
		}
		token, ok := a.Tokens.Lookup(key)
		if !ok {
			if a.Stats != nil {
				a.Stats.IncAuthFailed()
			}
			writeErr(w, http.StatusUnauthorized, -32001, "invalid api token")
			return
		}
		ctx := gatewayctx.WithToken(r.Context(), token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type Chain struct {
	ChainID string
}

func (c Chain) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := gatewayctx.WithChainID(r.Context(), c.ChainID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type MethodFilter struct {
	Stats *stats.Collector
}

func (m MethodFilter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			writeErr(w, http.StatusBadRequest, -32700, "parse error")
			return
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))

		methods, err := jsonrpc.ParseMethods(body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, -32600, err.Error())
			return
		}
		token := gatewayctx.Token(r.Context())
		if err := filter.CheckMethods(token, methods); err != nil {
			slog.Warn("method_denied",
				"token", token.Key,
				"methods", methods,
				"err", err.Error(),
			)
			if m.Stats != nil {
				m.Stats.IncMethodDenied(stats.TokenMeta{
					Key:         token.Key,
					Name:        token.Name,
					Plan:        token.Plan,
					BillingFree: token.BillingFree,
					Enabled:     token.Enabled,
				})
			}
			writeErr(w, http.StatusForbidden, -32601, err.Error())
			return
		}
		ctx := gatewayctx.WithMethods(r.Context(), methods)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type RateLimit struct {
	Limiter *ratelimit.Limiter
	Stats   *stats.Collector
}

func (m RateLimit) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := gatewayctx.Token(r.Context())
		chainID := gatewayctx.ChainID(r.Context())
		clientIP := httputil.ClientIP(r)
		res := m.Limiter.Allow(r.Context(), token, chainID, clientIP)
		if !res.Allowed {
			if m.Stats != nil {
				m.Stats.IncRateLimited(stats.TokenMeta{
					Key:         token.Key,
					Name:        token.Name,
					Plan:        token.Plan,
					BillingFree: token.BillingFree,
					Enabled:     token.Enabled,
				})
			}
			writeErr(w, http.StatusTooManyRequests, -32005, "rate limited: "+res.Reason)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func LogRateLimitReject(entry model.RateLimitLog) {
	slog.Warn("rate_limited",
		"token", entry.TokenKey,
		"name", entry.TokenName,
		"chain", entry.ChainID,
		"ip", entry.ClientIP,
		"reason", entry.Reason,
		"at", entry.At.Format(time.RFC3339),
	)
}

func writeErr(w http.ResponseWriter, status, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      nil,
		"error": map[string]interface{}{
			"code":    code,
			"message": msg,
		},
	})
}
