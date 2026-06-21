CREATE TABLE tenant_template (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id     BIGINT       NOT NULL,
    name          VARCHAR(128) NOT NULL,
    description   VARCHAR(512),
    bundle        JSONB        NOT NULL,
    source_app_id BIGINT,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT fk_tenant_template_tenant FOREIGN KEY (tenant_id) REFERENCES tenant(id),
    CONSTRAINT uq_tenant_template_name   UNIQUE (tenant_id, name)
);
CREATE INDEX idx_tenant_template_tenant ON tenant_template (tenant_id);
