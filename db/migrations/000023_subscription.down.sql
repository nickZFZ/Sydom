-- M6-billing-1 回滚（对称；tenant.plan_id 未动故无需恢复）。
DROP TABLE IF EXISTS subscription;
ALTER TABLE plan DROP COLUMN IF EXISTS currency;
ALTER TABLE plan DROP COLUMN IF EXISTS billing_period;
ALTER TABLE plan DROP COLUMN IF EXISTS price_cents;
