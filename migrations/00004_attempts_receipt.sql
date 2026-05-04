-- +goose Up
ALTER TABLE tx_attempts
    ADD COLUMN block_number        BIGINT,
    ADD COLUMN gas_used            BIGINT,
    ADD COLUMN effective_gas_price NUMERIC(78, 0),
    ADD COLUMN logs                JSONB,
    ADD COLUMN revert_reason       TEXT;

-- +goose Down
ALTER TABLE tx_attempts
    DROP COLUMN block_number,
    DROP COLUMN gas_used,
    DROP COLUMN effective_gas_price,
    DROP COLUMN logs,
    DROP COLUMN revert_reason;
