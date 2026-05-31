CREATE TABLE data_policy (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id       BIGINT       NOT NULL,
    subject_type VARCHAR(8)   NOT NULL,
    subject_id   VARCHAR(128) NOT NULL,
    resource     VARCHAR(128) NOT NULL,
    condition    JSONB        NOT NULL,
    description  VARCHAR(512),
    version      BIGINT       NOT NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT fk_data_policy_application FOREIGN KEY (app_id) REFERENCES application(id)
);
CREATE INDEX idx_data_policy_subject  ON data_policy (app_id, subject_type, subject_id);
CREATE INDEX idx_data_policy_resource ON data_policy (app_id, resource);
