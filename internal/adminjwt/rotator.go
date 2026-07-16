package adminjwt

import (
	"context"
	"log/slog"
	"time"
)

type RotatorConfig struct {
	Every            time.Duration
	OnStart          bool
	RevokePrevious   bool
	CleanupRetention time.Duration // 过期多久后删除
	LogToken         bool          // 是否在日志打印完整 JWT（仅建议本地）
}

// Rotator 定时签发 JWT 并写入 Store。
type Rotator struct {
	Issuer Issuer
	Store  Store
	Cfg    RotatorConfig
}

func (r *Rotator) Start(ctx context.Context) {
	if r.Cfg.OnStart {
		if err := r.Rotate(ctx); err != nil {
			slog.Error("admin jwt initial rotate failed", "err", err)
		}
	}
	if r.Cfg.Every <= 0 {
		return
	}
	t := time.NewTicker(r.Cfg.Every)
	go func() {
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := r.Rotate(ctx); err != nil {
					slog.Error("admin jwt rotate failed", "err", err)
				}
			}
		}
	}()
}

func (r *Rotator) Rotate(ctx context.Context) error {
	if r.Cfg.RevokePrevious {
		if err := r.Store.RevokeAllActive(ctx); err != nil {
			return err
		}
	}
	rec, err := r.Issuer.Issue()
	if err != nil {
		return err
	}
	if err := r.Store.Save(ctx, rec); err != nil {
		return err
	}
	slog.Info("admin jwt issued",
		"jti", rec.JTI,
		"subject", rec.Subject,
		"expires_at", rec.ExpiresAt.Format(time.RFC3339),
	)
	if r.Cfg.LogToken {
		slog.Info("admin jwt token ready", "token", rec.Token)
	}

	if r.Cfg.CleanupRetention > 0 {
		cutoff := time.Now().UTC().Add(-r.Cfg.CleanupRetention)
		if n, err := r.Store.CleanupExpired(ctx, cutoff); err != nil {
			slog.Warn("admin jwt cleanup failed", "err", err)
		} else if n > 0 {
			slog.Info("admin jwt cleaned expired", "deleted", n)
		}
	}
	return nil
}
