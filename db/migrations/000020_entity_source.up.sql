-- 为 IaC 治理域引入来源维度（对齐 permission 既有 source）。既有行默认 manual，向后兼容。
ALTER TABLE role        ADD COLUMN source VARCHAR(8) NOT NULL DEFAULT 'manual';
ALTER TABLE data_policy ADD COLUMN source VARCHAR(8) NOT NULL DEFAULT 'manual';
