-- Codex OAuth 指纹内部状态与账号配置分离，避免 seed 进入 extra、普通 API 和导出。
ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS codex_fingerprint_seed VARCHAR(64),
    ADD COLUMN IF NOT EXISTS codex_fingerprint_version VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS codex_fingerprint_epoch BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS codex_fingerprint_epoch_started_at TIMESTAMPTZ;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'accounts_codex_fingerprint_seed_format_check'
    ) THEN
        ALTER TABLE accounts
            ADD CONSTRAINT accounts_codex_fingerprint_seed_format_check
            CHECK (
                codex_fingerprint_seed IS NULL
                OR codex_fingerprint_seed ~ '^[0-9a-f]{64}$'
            );
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'accounts_codex_fingerprint_version_check'
    ) THEN
        ALTER TABLE accounts
            ADD CONSTRAINT accounts_codex_fingerprint_version_check
            CHECK (codex_fingerprint_version IN ('', 'v2'));
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'accounts_codex_fingerprint_epoch_nonnegative_check'
    ) THEN
        ALTER TABLE accounts
            ADD CONSTRAINT accounts_codex_fingerprint_epoch_nonnegative_check
            CHECK (codex_fingerprint_epoch >= 0);
    END IF;
END
$$;

COMMENT ON COLUMN accounts.codex_fingerprint_seed IS 'Codex 指纹 256-bit 随机种子（hex），禁止进入 extra 或普通导出';
COMMENT ON COLUMN accounts.codex_fingerprint_version IS 'Codex 指纹算法版本；空值表示尚未初始化';
COMMENT ON COLUMN accounts.codex_fingerprint_epoch IS 'Codex 账号级 Session epoch，与用户粘性 generation 无关';
COMMENT ON COLUMN accounts.codex_fingerprint_epoch_started_at IS '当前 Codex Session epoch 的起始时间';
