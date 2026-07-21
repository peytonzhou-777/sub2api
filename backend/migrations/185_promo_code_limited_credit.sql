-- 优惠码支持普通余额与限时额度奖励，并保存兑换时的奖励快照。
ALTER TABLE promo_codes
    ADD COLUMN IF NOT EXISTS reward_type VARCHAR(20) NOT NULL DEFAULT 'balance',
    ADD COLUMN IF NOT EXISTS validity_days INTEGER NOT NULL DEFAULT 0;

ALTER TABLE promo_code_usages
    ADD COLUMN IF NOT EXISTS reward_type VARCHAR(20) NOT NULL DEFAULT 'balance',
    ADD COLUMN IF NOT EXISTS validity_days INTEGER NOT NULL DEFAULT 0;

UPDATE promo_codes SET reward_type = 'balance' WHERE reward_type IS NULL OR reward_type = '';
UPDATE promo_codes SET validity_days = 0 WHERE reward_type = 'balance' AND validity_days IS NULL;
UPDATE promo_code_usages SET reward_type = 'balance' WHERE reward_type IS NULL OR reward_type = '';
UPDATE promo_code_usages SET validity_days = 0 WHERE reward_type = 'balance' AND validity_days IS NULL;

ALTER TABLE promo_codes
    DROP CONSTRAINT IF EXISTS promo_codes_reward_type_validity_days_check;
ALTER TABLE promo_codes
    ADD CONSTRAINT promo_codes_reward_type_validity_days_check
    CHECK ((reward_type = 'balance' AND validity_days = 0)
        OR (reward_type = 'limited_credit' AND validity_days BETWEEN 1 AND 36500));

ALTER TABLE promo_code_usages
    DROP CONSTRAINT IF EXISTS promo_code_usages_reward_type_validity_days_check;
ALTER TABLE promo_code_usages
    ADD CONSTRAINT promo_code_usages_reward_type_validity_days_check
    CHECK ((reward_type = 'balance' AND validity_days = 0)
        OR (reward_type = 'limited_credit' AND validity_days BETWEEN 1 AND 36500));

ALTER TABLE promo_codes
    DROP CONSTRAINT IF EXISTS promo_codes_limited_credit_amount_check;
ALTER TABLE promo_codes
    ADD CONSTRAINT promo_codes_limited_credit_amount_check
    CHECK (reward_type <> 'limited_credit' OR bonus_amount > 0);

ALTER TABLE promo_code_usages
    DROP CONSTRAINT IF EXISTS promo_code_usages_limited_credit_amount_check;
ALTER TABLE promo_code_usages
    ADD CONSTRAINT promo_code_usages_limited_credit_amount_check
    CHECK (reward_type <> 'limited_credit' OR bonus_amount > 0);
