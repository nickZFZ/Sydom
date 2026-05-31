CREATE TABLE application (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id       BIGINT       NOT NULL REFERENCES tenant(id),
    domain          VARCHAR(64)  NOT NULL,
    name            VARCHAR(128) NOT NULL,
    app_key         VARCHAR(64)  NOT NULL,
    app_secret_hash VARCHAR(255) NOT NULL,
    current_version BIGINT       NOT NULL DEFAULT 0,
    status          SMALLINT     NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uq_application_app_key       UNIQUE (app_key),
    CONSTRAINT uq_application_tenant_domain UNIQUE (tenant_id, domain)
);
