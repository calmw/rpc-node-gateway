-- Demo seed data (optional). Apply after 001_init.sql.
-- psql "$DATABASE_URL" -f migrations/002_seed.sql

INSERT INTO users (id, email, name, status)
VALUES
    ('11111111-1111-1111-1111-111111111111', 'free@example.com', 'Demo Free User', 'active'),
    ('22222222-2222-2222-2222-222222222222', 'pro@example.com', 'Demo Pro User', 'active')
ON CONFLICT (email) DO NOTHING;

INSERT INTO plans (
    code, name,
    token_ip_rate_limit_per_second, token_ip_rate_limit_burst,
    token_rate_limit_per_second, token_rate_limit_burst,
    daily_quota, denied_methods, price_per_success_cents
) VALUES
    (
        'free', 'Free',
        3, 6, 5, 10, 10000,
        '["eth_sendRawTransaction","personal_sendTransaction","debug_traceTransaction","debug_traceCall"]'::jsonb,
        0
    ),
    (
        'pro', 'Pro',
        30, 60, 50, 100, 1000000,
        '["personal_sendTransaction","debug_traceTransaction"]'::jsonb,
        1
    )
ON CONFLICT (code) DO UPDATE SET
    name = EXCLUDED.name,
    token_ip_rate_limit_per_second = EXCLUDED.token_ip_rate_limit_per_second,
    token_ip_rate_limit_burst = EXCLUDED.token_ip_rate_limit_burst,
    token_rate_limit_per_second = EXCLUDED.token_rate_limit_per_second,
    token_rate_limit_burst = EXCLUDED.token_rate_limit_burst,
    daily_quota = EXCLUDED.daily_quota,
    denied_methods = EXCLUDED.denied_methods,
    price_per_success_cents = EXCLUDED.price_per_success_cents,
    updated_at = now();

INSERT INTO api_tokens (user_id, token_key, name, plan_code, enabled, billing_free)
VALUES
    (
        '11111111-1111-1111-1111-111111111111',
        'demo-free-token', 'demo-free', 'free', true, true
    ),
    (
        '22222222-2222-2222-2222-222222222222',
        'demo-pro-token', 'demo-pro', 'pro', true, false
    )
ON CONFLICT (token_key) DO UPDATE SET
    name = EXCLUDED.name,
    plan_code = EXCLUDED.plan_code,
    enabled = EXCLUDED.enabled,
    billing_free = EXCLUDED.billing_free,
    updated_at = now();
