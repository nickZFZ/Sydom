-- M6-billing-1：plan 定价列 + subscription 生命周期实体（供应商无关计费地基第一片）。

-- plan 定价列。DEFAULT 使既有 free/pro 行平滑加列；不 seed 具体价格（AD-4，运营后置设真值）。
ALTER TABLE plan ADD COLUMN price_cents    BIGINT      NOT NULL DEFAULT 0;
ALTER TABLE plan ADD COLUMN billing_period VARCHAR(16) NOT NULL DEFAULT 'month'
    CONSTRAINT ck_plan_billing_period CHECK (billing_period IN ('month','year'));
ALTER TABLE plan ADD COLUMN currency       CHAR(3)     NOT NULL DEFAULT 'CNY';

-- subscription 生命周期实体（不含 plan_id：套餐真相源保持 tenant.plan_id，配额读点零改）。
CREATE TABLE subscription (
    id                    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id             BIGINT      NOT NULL REFERENCES tenant(id),
    status                VARCHAR(16) NOT NULL DEFAULT 'active',
    current_period_start  TIMESTAMPTZ NOT NULL DEFAULT now(),
    current_period_end    TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_subscription_tenant UNIQUE (tenant_id),
    CONSTRAINT ck_subscription_status CHECK (status IN ('active','trialing','past_due','canceled'))
);

-- 回填：迁移时存在的每个 tenant 建一条 active 订阅。
INSERT INTO subscription (tenant_id) SELECT id FROM tenant;
