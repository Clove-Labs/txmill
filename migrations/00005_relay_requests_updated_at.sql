-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION set_updated_at_column() RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

ALTER TABLE relay_requests
    ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE TRIGGER relay_requests_set_updated_at
BEFORE UPDATE ON relay_requests
FOR EACH ROW EXECUTE FUNCTION set_updated_at_column();

-- +goose Down
DROP TRIGGER IF EXISTS relay_requests_set_updated_at ON relay_requests;
ALTER TABLE relay_requests DROP COLUMN updated_at;
DROP FUNCTION IF EXISTS set_updated_at_column();
