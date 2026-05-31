CREATE TABLE casbin_rule (
    id      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id  BIGINT       NOT NULL,
    ptype   VARCHAR(8)   NOT NULL,
    v0      VARCHAR(128) NOT NULL DEFAULT '',
    v1      VARCHAR(128) NOT NULL DEFAULT '',
    v2      VARCHAR(128) NOT NULL DEFAULT '',
    v3      VARCHAR(128) NOT NULL DEFAULT '',
    v4      VARCHAR(128) NOT NULL DEFAULT '',
    v5      VARCHAR(128) NOT NULL DEFAULT '',
    version BIGINT       NOT NULL,
    CONSTRAINT uq_casbin_rule UNIQUE (app_id, ptype, v0, v1, v2, v3, v4, v5)
);
CREATE INDEX idx_casbin_rule_app_ptype   ON casbin_rule (app_id, ptype);
CREATE INDEX idx_casbin_rule_app_version ON casbin_rule (app_id, version);
