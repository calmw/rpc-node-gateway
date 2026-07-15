package auth

import (
	"fmt"
	"sync"

	"github.com/cisco/rpc-node-gateway/internal/config"
	"github.com/cisco/rpc-node-gateway/internal/model"
)

// Store 内存 Token 仓库（可替换为 DB + 缓存）。
type Store struct {
	mu     sync.RWMutex
	tokens map[string]*model.Token
}

func NewStore(cfg *config.Config) (*Store, error) {
	tokens := make(map[string]*model.Token, len(cfg.Tokens))
	for _, t := range cfg.Tokens {
		plan, ok := cfg.Plans[t.Plan]
		if !ok {
			return nil, fmt.Errorf("unknown plan %q for token %q", t.Plan, t.Key)
		}
		denied := make(map[string]struct{}, len(plan.DeniedMethods))
		for _, m := range plan.DeniedMethods {
			denied[m] = struct{}{}
		}
		tokens[t.Key] = &model.Token{
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
	return &Store{tokens: tokens}, nil
}

func (s *Store) Lookup(key string) (*model.Token, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tokens[key]
	if !ok || !t.Enabled {
		return nil, false
	}
	cp := *t
	return &cp, true
}
