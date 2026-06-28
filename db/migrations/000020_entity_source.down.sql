-- 移除 role / data_policy 的 source 列（回滚 000020）。
ALTER TABLE data_policy DROP COLUMN source;
ALTER TABLE role        DROP COLUMN source;
