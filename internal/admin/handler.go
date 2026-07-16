package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cisco/rpc-node-gateway/internal/adminjwt"
	"github.com/cisco/rpc-node-gateway/internal/auth"
	"github.com/cisco/rpc-node-gateway/internal/balancer"
	"github.com/cisco/rpc-node-gateway/internal/config"
	"github.com/cisco/rpc-node-gateway/internal/stats"
	"github.com/go-chi/chi/v5"
)

type Handler struct {
	Stats      *stats.Collector
	Tokens     *auth.Store
	Validator  *adminjwt.Validator
	Registry   *balancer.Registry
	ConfigPath string
}

func (h Handler) Routes(r chi.Router) {
	r.Route("/admin", func(r chi.Router) {
		r.Use(h.auth)
		r.Get("/stats", h.overview)
		r.Get("/stats/tokens", h.listTokens)
		r.Get("/stats/tokens/{token}", h.tokenStats)
		r.Get("/tokens", h.listTokens)
		r.Get("/upstreams", h.upstreams)
		r.Post("/upstreams/reload", h.reloadUpstreams)
	})
}

func (h Handler) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.Validator == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "jwt auth not configured"})
			return
		}
		raw := extractBearer(r)
		if raw == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing bearer token"})
			return
		}
		if _, err := h.Validator.Validate(r.Context(), raw); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or revoked jwt"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractBearer(r *http.Request) string {
	authz := r.Header.Get("Authorization")
	if len(authz) >= 7 && strings.EqualFold(authz[:7], "Bearer ") {
		return strings.TrimSpace(authz[7:])
	}
	return strings.TrimSpace(r.URL.Query().Get("access_token"))
}

func (h Handler) overview(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.Stats.Snapshot(h.Tokens.ListMeta()))
}

func (h Handler) listTokens(w http.ResponseWriter, _ *http.Request) {
	ov := h.Stats.Snapshot(h.Tokens.ListMeta())
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"generated_at": ov.GeneratedAt,
		"token_count":  ov.TokenCount,
		"tokens":       ov.Tokens,
	})
}

func (h Handler) tokenStats(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "token")
	h.Stats.RegisterTokens(h.Tokens.ListMeta())
	snap, ok := h.Stats.TokenSnapshot(key)
	if !ok {
		if meta, found := findMeta(h.Tokens.ListMeta(), key); found {
			snap = stats.TokenSnapshot{
				TokenKey:    meta.Key,
				TokenName:   meta.Name,
				Plan:        meta.Plan,
				BillingFree: meta.BillingFree,
				Enabled:     meta.Enabled,
				Metrics:     stats.Metrics{},
			}
			writeJSON(w, http.StatusOK, snap)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "token not found"})
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (h Handler) upstreams(w http.ResponseWriter, _ *http.Request) {
	if h.Registry == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "registry unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"chains": h.Registry.Snapshot(),
		"note":   "networks(chain ids) are fixed at startup; only nodes inside each chain can be scaled",
	})
}

func (h Handler) reloadUpstreams(w http.ResponseWriter, _ *http.Request) {
	if h.Registry == nil || h.ConfigPath == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "reload unavailable"})
		return
	}
	cfg, err := config.Load(h.ConfigPath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := h.Registry.ReloadNodes(cfg.Chains); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":     true,
		"chains": h.Registry.Snapshot(),
	})
}

func findMeta(list []stats.TokenMeta, key string) (stats.TokenMeta, bool) {
	for _, m := range list {
		if m.Key == key {
			return m, true
		}
	}
	return stats.TokenMeta{}, false
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
