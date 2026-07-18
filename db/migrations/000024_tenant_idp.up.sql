-- M6-sso-1：每租户 OIDC IdP 配置（一租户一 IdP）。client_secret 加密存储（AES-256-GCM）。
CREATE TABLE tenant_idp (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id          BIGINT      NOT NULL REFERENCES tenant(id),
    issuer             TEXT        NOT NULL,
    client_id          TEXT        NOT NULL,
    client_secret_enc  BYTEA       NOT NULL,
    enabled            BOOLEAN     NOT NULL DEFAULT false,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_tenant_idp_tenant UNIQUE (tenant_id)
);

-- email 域路由（下一片按域路由登录）；全局 UNIQUE 保「一域→一租户 IdP」不歧义。
CREATE TABLE tenant_idp_domain (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id  BIGINT NOT NULL REFERENCES tenant(id),
    domain     TEXT   NOT NULL,
    CONSTRAINT uq_tenant_idp_domain UNIQUE (domain)
);
