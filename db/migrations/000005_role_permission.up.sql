CREATE TABLE role_permission (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id        BIGINT      NOT NULL,
    role_id       BIGINT      NOT NULL,
    permission_id BIGINT      NOT NULL,
    eft           VARCHAR(8)  NOT NULL DEFAULT 'allow',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_role_permission_application FOREIGN KEY (app_id)        REFERENCES application(id),
    CONSTRAINT fk_role_permission_role       FOREIGN KEY (role_id)       REFERENCES role(id),
    CONSTRAINT fk_role_permission_permission FOREIGN KEY (permission_id) REFERENCES permission(id),
    CONSTRAINT uq_role_permission UNIQUE (app_id, role_id, permission_id)
);
