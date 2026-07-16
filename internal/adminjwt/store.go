package adminjwt

import (
	"context"
	"fmt"
	"time"
)

// Record 持久化的 Admin JWT。
type Record struct {
	ID        string
	JTI       string
	Token     string
	Subject   string
	IssuedAt  time.Time
	ExpiresAt time.Time
	Revoked   bool
}

// Store 抽象：PostgreSQL 或进程内实现。
type Store interface {
	Save(ctx context.Context, rec Record) error
	IsActive(ctx context.Context, jti string) (bool, error)
	RevokeAllActive(ctx context.Context) error
	LatestActive(ctx context.Context) (*Record, error)
	CleanupExpired(ctx context.Context, before time.Time) (int64, error)
}

var ErrNotFound = fmt.Errorf("admin jwt not found")
