package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const openAIAccountPersonaArchitectureVersion = "account_persona_v1"

// EnsureOpenAIAccountPersonaArchitectureReady 阻止新运行时在数据迁移完成前启动。
func EnsureOpenAIAccountPersonaArchitectureReady(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("OpenAI AccountPersona architecture check requires database")
	}
	var state, version string
	err := db.QueryRowContext(ctx, `SELECT state, architecture_version
FROM openai_persona_architecture_state WHERE singleton = TRUE`).Scan(&state, &version)
	if err != nil {
		return fmt.Errorf("read OpenAI AccountPersona architecture state: %w", err)
	}
	if state == "ready" && version == openAIAccountPersonaArchitectureVersion {
		return nil
	}
	var accountCount int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts
WHERE platform = 'openai' AND type = 'oauth' AND deleted_at IS NULL`).Scan(&accountCount); err != nil {
		return fmt.Errorf("count OpenAI OAuth accounts: %w", err)
	}
	if accountCount == 0 {
		_, err = db.ExecContext(ctx, `UPDATE openai_persona_architecture_state
SET architecture_version = $1, state = 'ready', migration_report = '{"fresh_database":true}'::jsonb, updated_at = NOW()
WHERE singleton = TRUE`, openAIAccountPersonaArchitectureVersion)
		return err
	}
	return fmt.Errorf("OpenAI AccountPersona migration is pending; stop all instances and run openai-persona-migrate --apply --confirm ACCOUNT_PERSONA_V1")
}
