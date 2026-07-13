CREATE TABLE plan (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name             VARCHAR(64) NOT NULL UNIQUE,
    max_applications INTEGER     NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO plan (name, max_applications) VALUES ('free', 3), ('pro', 50);
ALTER TABLE tenant ADD COLUMN plan_id BIGINT NOT NULL DEFAULT 1 REFERENCES plan(id);
