package auth

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/cisco/rpc-node-gateway/internal/config"
	"github.com/cisco/rpc-node-gateway/internal/db"
	"github.com/cisco/rpc-node-gateway/internal/model"
	"github.com/cisco/rpc-node-gateway/internal/stats"
)

// Store 内存 Token 缓存；可从 YAML 或 PostgreSQL 加载，并支持定时刷新。
type Store struct {
	mu     sync.RWMutex
	tokens map[string]*model.Token
}

func newEmptyStore() *Store {
	return &Store{tokens: make(map[string]*model.Token)}
}

// NewStoreFromConfig 从 YAML 的 plans/tokens 构建（database.enabled=false 时使用）。
func NewStoreFromConfig(cfg *config.Config) (*Store, error) {
	tokens := make(map[string]*model.Token, len(cfg.Tokens))
	for _, t := range cfg.Tokens {
		plan, ok := cfg.Plans[t.Plan]
		if !ok {
			return nil, fmt.Errorf("unknown plan %q for token %q", t.Plan, t.Key)
		}
		tokens[t.Key] = tokenFromYAML(t, plan)
	}
	return &Store{tokens: tokens}, nil
}

// NewStoreFromDB 从 PostgreSQL 加载启用中的 Token，并可选定时刷新。
func NewStoreFromDB(ctx context.Context, pg *db.Postgres, refreshEvery time.Duration) (*Store, error) {
	s := newEmptyStore()
	if err := s.reload(ctx, pg); err != nil {
		return nil, err
	}
	if refreshEvery > 0 {
		go s.loopRefresh(ctx, pg, refreshEvery)
	}
	return s, nil
}

func (s *Store) Lookup(key string) (*model.Token, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tokens[key]
	if !ok || !t.Enabled {
		return nil, false
	}
	cp := *t
	if t.DeniedMethods != nil {
		cp.DeniedMethods = make(map[string]struct{}, len(t.DeniedMethods))
		for m := range t.DeniedMethods {
			cp.DeniedMethods[m] = struct{}{}
		}
	}
	return &cp, true
}

func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tokens)
}

// ListMeta 返回当前缓存中全部 Token 元信息（含 disabled 的不会出现，因加载时已过滤启用项；YAML 模式含 enabled=false）。
func (s *Store) ListMeta() []stats.TokenMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]stats.TokenMeta, 0, len(s.tokens))
	for _, t := range s.tokens {
		out = append(out, stats.TokenMeta{
			Key:         t.Key,
			Name:        t.Name,
			Plan:        t.Plan,
			BillingFree: t.BillingFree,
			Enabled:     t.Enabled,
		})
	}
	return out
}

func (s *Store) reload(ctx context.Context, pg *db.Postgres) error {
	list, err := pg.ListEnabledTokens(ctx)
	if err != nil {
		return err
	}
	next := make(map[string]*model.Token, len(list))
	for _, t := range list {
		next[t.Key] = t
	}
	s.mu.Lock()
	s.tokens = next
	s.mu.Unlock()
	slog.Info("token store reloaded from database", "count", len(next))
	return nil
}

func (s *Store) loopRefresh(ctx context.Context, pg *db.Postgres, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := s.reload(rctx, pg); err != nil {
				slog.Error("token store refresh failed", "err", err)
			}
			cancel()
		}
	}
}

func tokenFromYAML(t config.TokenConfig, plan config.Plan) *model.Token {
	denied := make(map[string]struct{}, len(plan.DeniedMethods))
	for _, m := range plan.DeniedMethods {
		denied[m] = struct{}{}
	}
	return &model.Token{
		Key:                       t.Key,
		Name:                      t.Name,
		Plan:                      t.Plan,
		Enabled:                   t.Enabled,
		BillingFree:               t.BillingFree,
		TokenRateLimitPerSecond:   plan.TokenRateLimitPerSecond,
		TokenRateLimitBurst:       plan.TokenRateLimitBurst,
		TokenIPRateLimitPerSecond: plan.TokenIPRateLimitPerSecond,
		TokenIPRateLimitBurst:     plan.TokenIPRateLimitBurst,
		DailyQuota:                plan.DailyQuota,
		DeniedMethods:             denied,
	}
}
