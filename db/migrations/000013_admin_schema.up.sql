CREATE TABLE admin_operator (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    principal   VARCHAR(128) NOT NULL UNIQUE,
    secret_enc  BYTEA        NOT NULL,
    status      SMALLINT     NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE admin_role (
    id    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code  VARCHAR(64)  NOT NULL UNIQUE,
    name  VARCHAR(128) NOT NULL
);

CREATE TABLE admin_role_grant (
    id        BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    role_id   BIGINT      NOT NULL,
    domain    VARCHAR(64) NOT NULL,
    resource  VARCHAR(64) NOT NULL,
    action    VARCHAR(32) NOT NULL,
    CONSTRAINT fk_admin_role_grant_role FOREIGN KEY (role_id) REFERENCES admin_role(id) ON DELETE CASCADE,
    CONSTRAINT uq_admin_role_grant      UNIQUE (role_id, domain, resource, action)
);

CREATE TABLE admin_subject_role (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    operator_id BIGINT      NOT NULL,
    role_id     BIGINT      NOT NULL,
    domain      VARCHAR(64) NOT NULL,
    CONSTRAINT fk_admin_subject_role_operator FOREIGN KEY (operator_id) REFERENCES admin_operator(id) ON DELETE CASCADE,
    CONSTRAINT fk_admin_subject_role_role     FOREIGN KEY (role_id)     REFERENCES admin_role(id)     ON DELETE CASCADE,
    CONSTRAINT uq_admin_subject_role          UNIQUE (operator_id, role_id, domain)
);

CREATE TABLE admin_policy_version (
    id      SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    version BIGINT   NOT NULL DEFAULT 0
);
INSERT INTO admin_policy_version (id, version) VALUES (1, 0);

-- 内置 super-admin：在 * 域拥有全资源全动作（matcher 用通配处理）
INSERT INTO admin_role (code, name) VALUES ('super-admin', '超级管理员');
INSERT INTO admin_role_grant (role_id, domain, resource, action)
    SELECT id, '*', '*', '*' FROM admin_role WHERE code='super-admin';
