-- M6-sso-2：operator 关联 email，供 OIDC 登录严格映射（email_verified 匹配）。
-- nullable + 全局 UNIQUE（一 email→一 operator）；非 SSO operator 为 NULL；小写化由应用层保证。
ALTER TABLE admin_operator ADD COLUMN email VARCHAR(320) UNIQUE;
