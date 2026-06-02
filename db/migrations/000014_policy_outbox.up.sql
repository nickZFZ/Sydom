CREATE TABLE policy_outbox (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id       BIGINT      NOT NULL,
    version      BIGINT      NOT NULL,
    delta_proto  BYTEA       NOT NULL,
    published_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_policy_outbox_unpublished ON policy_outbox (id) WHERE published_at IS NULL;
