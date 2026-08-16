-- 网络安全保证金第七阶段：允许自动原路退款在人工核验后记录完整的外部退款事实。
-- 外部退款编号、时间和凭证必须同时为空或同时存在，避免形成部分证据状态。

ALTER TABLE security_deposit_refunds
    DROP CONSTRAINT IF EXISTS security_deposit_refunds_external_shape_check;

ALTER TABLE security_deposit_refunds
    ADD CONSTRAINT security_deposit_refunds_external_shape_check CHECK (
        (
            external_refund_id IS NULL
            AND external_refunded_at IS NULL
            AND external_evidence IS NULL
        )
        OR (
            external_refund_id IS NOT NULL
            AND external_refunded_at IS NOT NULL
            AND external_evidence IS NOT NULL
        )
    );
