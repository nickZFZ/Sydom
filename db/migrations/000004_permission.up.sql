CREATE TABLE permission (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id      BIGINT       NOT NULL,
    code        VARCHAR(255) NOT NULL,
    resource    VARCHAR(128) NOT NULL,
    action      VARCHAR(64)  NOT NULL,
    type        VARCHAR(16)  NOT NULL,
    name        VARCHAR(128) NOT NULL,
    description VARCHAR(512),
    source      VARCHAR(8)   NOT NULL DEFAULT 'manual',
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT fk_permission_application FOREIGN KEY (app_id) REFERENCES application(id),
    CONSTRAINT uq_permission_app_code    UNIQUE (app_id, code)
);
