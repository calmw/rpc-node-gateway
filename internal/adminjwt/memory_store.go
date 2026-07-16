package adminjwt

import (
	"context"
	"sync"
	"time"
)

// MemoryStore 本地无库时的进程内存储（重启丢失）。
type MemoryStore struct {
	mu   sync.RWMutex
	byJTI map[string]Record
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byJTI: make(map[string]Record)}
}

func (s *MemoryStore) Save(_ context.Context, rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byJTI[rec.JTI] = rec
	return nil
}

func (s *MemoryStore) IsActive(_ context.Context, jti string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.byJTI[jti]
	if !ok || rec.Revoked {
		return false, nil
	}
	return time.Now().Before(rec.ExpiresAt), nil
}

func (s *MemoryStore) RevokeAllActive(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, rec := range s.byJTI {
		if !rec.Revoked && time.Now().Before(rec.ExpiresAt) {
			rec.Revoked = true
			s.byJTI[k] = rec
		}
	}
	return nil
}

func (s *MemoryStore) LatestActive(_ context.Context) (*Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *Record
	now := time.Now()
	for _, rec := range s.byJTI {
		r := rec
		if r.Revoked || !now.Before(r.ExpiresAt) {
			continue
		}
		if best == nil || r.IssuedAt.After(best.IssuedAt) {
			best = &r
		}
	}
	if best == nil {
		return nil, ErrNotFound
	}
	return best, nil
}

func (s *MemoryStore) CleanupExpired(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for k, rec := range s.byJTI {
		if !rec.ExpiresAt.After(before) {
			delete(s.byJTI, k)
			n++
		}
	}
	return n, nil
}
