package adminjwt

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cisco/rpc-node-gateway/internal/db"
	"github.com/jackc/pgx/v5"
)

type PostgresStore struct {
	db *db.Postgres
}

func NewPostgresStore(pg *db.Postgres) *PostgresStore {
	return &PostgresStore{db: pg}
}

func (s *PostgresStore) Save(ctx context.Context, rec Record) error {
	_, err := s.db.Pool.Exec(ctx, `
INSERT INTO admin_jwt_tokens (id, jti, token, subject, issued_at, expires_at, revoked)
VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
ON CONFLICT (jti) DO UPDATE SET
    token = EXCLUDED.token,
    subject = EXCLUDED.subject,
    issued_at = EXCLUDED.issued_at,
    expires_at = EXCLUDED.expires_at,
    revoked = EXCLUDED.revoked
`, rec.ID, rec.JTI, rec.Token, rec.Subject, rec.IssuedAt, rec.ExpiresAt, rec.Revoked)
	if err != nil {
		return fmt.Errorf("save admin jwt: %w", err)
	}
	return nil
}

func (s *PostgresStore) IsActive(ctx context.Context, jti string) (bool, error) {
	var ok bool
	err := s.db.Pool.QueryRow(ctx, `
SELECT EXISTS(
    SELECT 1 FROM admin_jwt_tokens
    WHERE jti = $1 AND revoked = false AND expires_at > now()
)
`, jti).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("check admin jwt: %w", err)
	}
	return ok, nil
}

func (s *PostgresStore) RevokeAllActive(ctx context.Context) error {
	_, err := s.db.Pool.Exec(ctx, `
UPDATE admin_jwt_tokens
SET revoked = true
WHERE revoked = false AND expires_at > now()
`)
	if err != nil {
		return fmt.Errorf("revoke admin jwt: %w", err)
	}
	return nil
}

func (s *PostgresStore) LatestActive(ctx context.Context) (*Record, error) {
	var rec Record
	err := s.db.Pool.QueryRow(ctx, `
SELECT id::text, jti, token, subject, issued_at, expires_at, revoked
FROM admin_jwt_tokens
WHERE revoked = false AND expires_at > now()
ORDER BY issued_at DESC
LIMIT 1
`).Scan(&rec.ID, &rec.JTI, &rec.Token, &rec.Subject, &rec.IssuedAt, &rec.ExpiresAt, &rec.Revoked)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("latest admin jwt: %w", err)
	}
	return &rec, nil
}

func (s *PostgresStore) CleanupExpired(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.db.Pool.Exec(ctx, `
DELETE FROM admin_jwt_tokens WHERE expires_at <= $1
`, before)
	if err != nil {
		return 0, fmt.Errorf("cleanup admin jwt: %w", err)
	}
	return tag.RowsAffected(), nil
}
