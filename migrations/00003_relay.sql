-- +goose Up
CREATE TABLE relay_requests (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    app_id             UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    chain_id           BIGINT NOT NULL,
    to_address         TEXT NOT NULL CHECK (to_address ~ '^0x[0-9a-f]{40}$'),
    call_data          BYTEA NOT NULL,
    value              NUMERIC(78, 0) NOT NULL DEFAULT 0,
    gas_limit          BIGINT,
    deadline           BIGINT,
    callback_url       TEXT,
    callback_metadata  TEXT,
    status             TEXT NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending', 'rejected', 'submitted', 'confirmed', 'reverted', 'failed')),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX relay_requests_app_id_created_idx ON relay_requests (app_id, created_at DESC);
CREATE INDEX relay_requests_open_status_idx ON relay_requests (status)
    WHERE status IN ('pending', 'submitted');

CREATE TABLE tx_attempts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id      UUID NOT NULL REFERENCES relay_requests(id) ON DELETE CASCADE,
    signer_address  TEXT NOT NULL CHECK (signer_address ~ '^0x[0-9a-f]{40}$'),
    nonce           BIGINT NOT NULL,
    tx_hash         TEXT NOT NULL CHECK (tx_hash ~ '^0x[0-9a-f]{64}$'),
    gas_price       NUMERIC(78, 0) NOT NULL,
    status          TEXT NOT NULL DEFAULT 'submitting'
                    CHECK (status IN ('submitting', 'submitted', 'failed', 'confirmed', 'reverted')),
    submitted_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX tx_attempts_request_id_idx ON tx_attempts (request_id);
CREATE UNIQUE INDEX tx_attempts_tx_hash_key ON tx_attempts (tx_hash);
CREATE INDEX tx_attempts_open_status_idx ON tx_attempts (status)
    WHERE status IN ('submitting', 'submitted');

-- +goose Down
DROP TABLE IF EXISTS tx_attempts;
DROP TABLE IF EXISTS relay_requests;
