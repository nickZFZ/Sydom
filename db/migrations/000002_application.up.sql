CREATE TABLE application (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id       BIGINT       NOT NULL,
    domain          VARCHAR(64)  NOT NULL,
    name            VARCHAR(128) NOT NULL,
    app_key         VARCHAR(64)  NOT NULL,
    app_secret_hash VARCHAR(255) NOT NULL,
    current_version BIGINT       NOT NULL DEFAULT 0,
    status          SMALLINT     NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT fk_application_tenant      FOREIGN KEY (tenant_id) REFERENCES tenant(id),
    CONSTRAINT uq_application_app_key       UNIQUE (app_key),
    CONSTRAINT uq_application_tenant_domain UNIQUE (tenant_id, domain)
);
