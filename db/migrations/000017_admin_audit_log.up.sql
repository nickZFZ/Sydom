CREATE TABLE admin_audit_log (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id     BIGINT,
    operator      VARCHAR(128) NOT NULL,
    action        VARCHAR(32)  NOT NULL,
    entity_type   VARCHAR(32)  NOT NULL,
    entity_id     VARCHAR(128),
    diff          JSONB,
    admin_version BIGINT,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_admin_audit_tenant_created ON admin_audit_log (tenant_id, created_at);
CREATE INDEX idx_admin_audit_tenant_entity  ON admin_audit_log (tenant_id, entity_type, entity_id);
