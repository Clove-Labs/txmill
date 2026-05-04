-- +goose Up
CREATE TABLE apps (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name                  TEXT NOT NULL,
    treasury_address      TEXT NOT NULL CHECK (treasury_address ~ '^0x[0-9a-f]{40}$'),
    bearer_token_hash     BYTEA NOT NULL,
    pool_size             INTEGER NOT NULL CHECK (pool_size > 0),
    default_callback_url  TEXT,
    disabled              BOOLEAN NOT NULL DEFAULT FALSE,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX apps_bearer_token_hash_key ON apps (bearer_token_hash);

CREATE TABLE signers (
    address       TEXT PRIMARY KEY CHECK (address ~ '^0x[0-9a-f]{40}$'),
    app_id        UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at  TIMESTAMPTZ
);

CREATE INDEX signers_app_id_idx ON signers (app_id);

-- +goose Down
DROP TABLE IF EXISTS signers;
DROP TABLE IF EXISTS apps;
