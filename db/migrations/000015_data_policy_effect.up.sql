ALTER TABLE data_policy
    ADD COLUMN effect VARCHAR(8) NOT NULL DEFAULT 'allow'
    CONSTRAINT data_policy_effect_chk CHECK (effect IN ('allow', 'deny'));
