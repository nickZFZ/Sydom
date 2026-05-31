CREATE TABLE role_inheritance (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id         BIGINT      NOT NULL,
    parent_role_id BIGINT      NOT NULL,
    child_role_id  BIGINT      NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT fk_role_inheritance_app    FOREIGN KEY (app_id)         REFERENCES application(id),
    CONSTRAINT fk_role_inheritance_parent FOREIGN KEY (parent_role_id) REFERENCES role(id),
    CONSTRAINT fk_role_inheritance_child  FOREIGN KEY (child_role_id)  REFERENCES role(id),
    CONSTRAINT uq_role_inheritance UNIQUE (app_id, parent_role_id, child_role_id)
);
