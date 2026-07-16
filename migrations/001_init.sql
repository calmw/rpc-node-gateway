-- rpc-node-gateway initial schema
-- Apply: psql "$DATABASE_URL" -f migrations/001_init.sql

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ---------------------------------------------------------------------------
-- users
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS users (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email       TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'disabled')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- plans (rate limit + method policy; shared by tokens)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS plans (
    code                          TEXT PRIMARY KEY,
    name                          TEXT NOT NULL DEFAULT '',
    token_ip_rate_limit_per_second INTEGER NOT NULL DEFAULT 0,
    token_ip_rate_limit_burst      INTEGER NOT NULL DEFAULT 0,
    token_rate_limit_per_second    INTEGER NOT NULL DEFAULT 0,
    token_rate_limit_burst         INTEGER NOT NULL DEFAULT 0,
    daily_quota                    BIGINT NOT NULL DEFAULT 0,
    denied_methods                 JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- 单价：每个成功 JSON-RPC 调用（最小货币单位，如分）；免费套餐可为 0
    price_per_success_cents        INTEGER NOT NULL DEFAULT 0,
    created_at                     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- api tokens
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS api_tokens (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL REFERENCES users(id),
    token_key     TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL DEFAULT '',
    plan_code     TEXT NOT NULL REFERENCES plans(code),
    enabled       BOOLEAN NOT NULL DEFAULT true,
    -- true=成功调用不计费，但仍限流
    billing_free  BOOLEAN NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT api_tokens_key_no_slash CHECK (position('/' IN token_key) = 0)
);

CREATE INDEX IF NOT EXISTS idx_api_tokens_user_id ON api_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_api_tokens_enabled ON api_tokens(enabled) WHERE enabled = true;

-- ---------------------------------------------------------------------------
-- daily usage aggregate (ledger-friendly; Redis 仍可做热路径计数)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS usage_daily (
    id              BIGSERIAL PRIMARY KEY,
    token_key       TEXT NOT NULL,
    day             DATE NOT NULL,
    success_count   BIGINT NOT NULL DEFAULT 0,
    billable_count  BIGINT NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (token_key, day)
);

CREATE INDEX IF NOT EXISTS idx_usage_daily_day ON usage_daily(day);

-- ---------------------------------------------------------------------------
-- billing events (idempotent raw success events from gateway)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS billing_events (
    event_id      TEXT PRIMARY KEY,
    token_key     TEXT NOT NULL,
    user_id       UUID,
    plan_code     TEXT,
    chain_id      TEXT NOT NULL DEFAULT '',
    methods       JSONB NOT NULL DEFAULT '[]'::jsonb,
    success_n     INTEGER NOT NULL CHECK (success_n > 0),
    billable      BOOLEAN NOT NULL DEFAULT true,
    amount_cents  INTEGER NOT NULL DEFAULT 0,
    upstream      TEXT NOT NULL DEFAULT '',
    latency_ms    BIGINT NOT NULL DEFAULT 0,
    client_ip     TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_billing_events_token_created
    ON billing_events(token_key, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_billing_events_user_created
    ON billing_events(user_id, created_at DESC)
    WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_billing_events_billable
    ON billing_events(billable, created_at DESC)
    WHERE billable = true;

-- ---------------------------------------------------------------------------
-- billing ledger entries (optional settlement / invoice lines)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS billing_ledger (
    id            BIGSERIAL PRIMARY KEY,
    user_id       UUID NOT NULL REFERENCES users(id),
    token_key     TEXT NOT NULL,
    period_start  DATE NOT NULL,
    period_end    DATE NOT NULL,
    success_count BIGINT NOT NULL DEFAULT 0,
    amount_cents  INTEGER NOT NULL DEFAULT 0,
    status        TEXT NOT NULL DEFAULT 'open'
                      CHECK (status IN ('open', 'invoiced', 'paid', 'void')),
    note          TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, token_key, period_start, period_end)
);

CREATE INDEX IF NOT EXISTS idx_billing_ledger_user_status
    ON billing_ledger(user_id, status);
