-- M6.1d 成员配额：plan 加 max_members（per-tenant 成员上限，数据驱动）。
-- DEFAULT 0 使 NOT NULL 列可加到既有行 → UPDATE 设真值 → DROP DEFAULT 对齐 max_applications（无默认）。
ALTER TABLE plan ADD COLUMN max_members INTEGER NOT NULL DEFAULT 0;
UPDATE plan SET max_members = 3  WHERE name = 'free';
UPDATE plan SET max_members = 25 WHERE name = 'pro';
ALTER TABLE plan ALTER COLUMN max_members DROP DEFAULT;
