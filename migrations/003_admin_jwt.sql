-- Admin JWT tokens for /admin/stats* authentication
-- Apply: psql "$DATABASE_URL" -f migrations/003_admin_jwt.sql

CREATE TABLE IF NOT EXISTS admin_jwt_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    jti         TEXT NOT NULL UNIQUE,
    token       TEXT NOT NULL,
    subject     TEXT NOT NULL DEFAULT 'admin-stats',
    issued_at   TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked     BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_admin_jwt_active
    ON admin_jwt_tokens (revoked, expires_at DESC)
    WHERE revoked = false;

CREATE INDEX IF NOT EXISTS idx_admin_jwt_jti_active
    ON admin_jwt_tokens (jti)
    WHERE revoked = false;
