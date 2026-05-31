CREATE TABLE user_role_binding (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id     BIGINT       NOT NULL,
    user_id    VARCHAR(128) NOT NULL,
    role_id    BIGINT       NOT NULL,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT fk_user_role_binding_application FOREIGN KEY (app_id)  REFERENCES application(id),
    CONSTRAINT fk_user_role_binding_role FOREIGN KEY (role_id) REFERENCES role(id),
    CONSTRAINT uq_user_role_binding UNIQUE (app_id, user_id, role_id)
);
