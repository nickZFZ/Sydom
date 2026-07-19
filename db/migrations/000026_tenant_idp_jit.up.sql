-- M6-sso-3：每租户 JIT 开关。默认 false=保留事前登录严格映射（向后兼容）。
ALTER TABLE tenant_idp ADD COLUMN jit_enabled BOOLEAN NOT NULL DEFAULT false;
