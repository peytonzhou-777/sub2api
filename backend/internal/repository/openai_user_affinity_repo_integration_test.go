//go:build integration

package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 该回归测试必须使用真实 PostgreSQL，sqlmock 无法触发无类型 NULL 的参数推断错误。
func TestRecordOpenAIUserAffinityCapacityFailureBeforeMigrationThreshold(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	user := mustCreateUser(t, client, &service.User{})
	account := mustCreateAccount(t, client, &service.Account{
		Name:     "openai-affinity-capacity-failure-" + time.Now().Format(time.RFC3339Nano),
		Platform: service.PlatformOpenAI,
	})
	scopeKey := "openai:v1:group:1:lane:general"
	generation := int64(3)

	_, err := tx.ExecContext(ctx, `
		INSERT INTO user_account_placements
			(user_id, scope_key, account_id, generation, status, expires_at)
		VALUES ($1, $2, $3, $4, 'active', $5)`,
		user.ID, scopeKey, account.ID, generation, time.Now().UTC().Add(time.Hour))
	require.NoError(t, err)

	config := service.DefaultOpenAIUserAffinityConfig()
	config.CapacityFailureMigrationThreshold = 2
	authorizedAt, err := repo.RecordOpenAIUserAffinityCapacityFailure(
		ctx, user.ID, account.ID, generation, scopeKey, strings.Repeat("a", 64), "resident_account_excluded", config,
	)
	require.NoError(t, err)
	require.Nil(t, authorizedAt)

	var failureCount, failureThreshold int
	var migrationAuthorizedAt sql.NullTime
	var status string
	err = scanSingleRow(ctx, tx, `
		SELECT failure_count, failure_threshold, migration_authorized_at, status
		FROM user_account_capacity_incidents
		WHERE user_id = $1 AND scope_key = $2 AND source_account_id = $3
		  AND placement_generation = $4`,
		[]any{user.ID, scopeKey, account.ID, generation},
		&failureCount, &failureThreshold, &migrationAuthorizedAt, &status)
	require.NoError(t, err)
	require.Equal(t, 1, failureCount)
	require.Equal(t, 2, failureThreshold)
	require.False(t, migrationAuthorizedAt.Valid)
	require.Equal(t, "collecting", status)
}
