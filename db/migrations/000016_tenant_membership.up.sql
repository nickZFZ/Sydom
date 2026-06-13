CREATE TABLE tenant_membership (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id   BIGINT      NOT NULL,
    operator_id BIGINT      NOT NULL,
    tier        SMALLINT    NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_tenant_membership_tenant   FOREIGN KEY (tenant_id)   REFERENCES tenant(id)         ON DELETE CASCADE,
    CONSTRAINT fk_tenant_membership_operator FOREIGN KEY (operator_id) REFERENCES admin_operator(id) ON DELETE CASCADE,
    CONSTRAINT uq_tenant_membership UNIQUE (tenant_id, operator_id)
);
CREATE INDEX idx_tenant_membership_operator ON tenant_membership(operator_id);
