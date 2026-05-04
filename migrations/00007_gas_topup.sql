-- +goose Up
ALTER TABLE apps
    ADD COLUMN signer_min_balance   NUMERIC(78, 0) NOT NULL DEFAULT 0,
    ADD COLUMN signer_refill_amount NUMERIC(78, 0) NOT NULL DEFAULT 0,
    ADD COLUMN treasury_min_balance NUMERIC(78, 0) NOT NULL DEFAULT 0;

CREATE TABLE gas_attempts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    app_id          UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    signer_address  TEXT NOT NULL CHECK (signer_address ~ '^0x[0-9a-f]{40}$'),
    nonce           BIGINT NOT NULL,
    tx_hash         TEXT NOT NULL CHECK (tx_hash ~ '^0x[0-9a-f]{64}$'),
    amount          NUMERIC(78, 0) NOT NULL,
    gas_price       NUMERIC(78, 0) NOT NULL,
    status          TEXT NOT NULL DEFAULT 'submitted'
                    CHECK (status IN ('submitted', 'confirmed', 'reverted', 'failed')),
    submitted_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX gas_attempts_app_id_idx ON gas_attempts (app_id);
CREATE UNIQUE INDEX gas_attempts_tx_hash_key ON gas_attempts (tx_hash);
CREATE INDEX gas_attempts_recent_signer_idx ON gas_attempts (signer_address, submitted_at DESC);

-- +goose Down
DROP TABLE IF EXISTS gas_attempts;
ALTER TABLE apps
    DROP COLUMN signer_min_balance,
    DROP COLUMN signer_refill_amount,
    DROP COLUMN treasury_min_balance;
