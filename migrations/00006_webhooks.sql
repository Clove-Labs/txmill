-- +goose Up
ALTER TABLE apps ADD COLUMN default_callback_secret TEXT;

CREATE TABLE webhook_deliveries (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id      UUID NOT NULL REFERENCES relay_requests(id) ON DELETE CASCADE,
    url             TEXT NOT NULL,
    payload         BYTEA NOT NULL,
    signature       TEXT NOT NULL,
    attempt         INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'delivered', 'dead')),
    response_code   INTEGER,
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX webhook_deliveries_due_idx
    ON webhook_deliveries (next_attempt_at)
    WHERE status = 'pending';
CREATE INDEX webhook_deliveries_request_id_idx ON webhook_deliveries (request_id);

-- +goose Down
DROP TABLE IF EXISTS webhook_deliveries;
ALTER TABLE apps DROP COLUMN default_callback_secret;
