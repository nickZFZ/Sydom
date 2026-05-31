-- 与 up 对称：忠实还原 000002 的原始定义 VARCHAR(255) NOT NULL（原列亦无默认值）。
-- 仅适用于空表回滚；非空表回滚须先 ADD 可空列、回填、再加 NOT NULL。
ALTER TABLE application DROP COLUMN app_secret_enc;
ALTER TABLE application ADD COLUMN app_secret_hash VARCHAR(255) NOT NULL;
