CREATE TABLE policy_audit_log (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id      BIGINT       NOT NULL,
    operator    VARCHAR(128) NOT NULL,
    action      VARCHAR(16)  NOT NULL,
    entity_type VARCHAR(32)  NOT NULL,
    entity_id   VARCHAR(128),
    diff        JSONB,
    version     BIGINT       NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_app_created ON policy_audit_log (app_id, created_at);
CREATE INDEX idx_audit_app_version ON policy_audit_log (app_id, version);
CREATE INDEX idx_audit_app_entity  ON policy_audit_log (app_id, entity_type, entity_id);
