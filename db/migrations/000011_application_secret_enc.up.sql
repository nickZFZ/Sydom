-- schema 尚未上线、application 表为空，故 ADD COLUMN ... NOT NULL 无需默认值。
-- 如将来需在非空表执行，须先 ADD 可空列、回填密文、再加 NOT NULL 约束。
ALTER TABLE application DROP COLUMN app_secret_hash;
ALTER TABLE application ADD COLUMN app_secret_enc BYTEA NOT NULL;
