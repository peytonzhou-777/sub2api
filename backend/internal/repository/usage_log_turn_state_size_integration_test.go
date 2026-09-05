//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestUsageLog_TurnStateSizePersistence 覆盖所有写入 SQL 的整数及 NULL 参数和查询回读。
func TestUsageLog_TurnStateSizePersistence(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newUsageLogRepositoryWithSQL(client, integrationDB)
	user := mustCreateUser(t, client, &service.User{Email: "state-size-" + uuid.NewString() + "@example.com"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-state-" + uuid.NewString(), Name: "k"})
	account := mustCreateAccount(t, client, &service.Account{Name: "state-size-" + uuid.NewString()})
	size := 4097
	for _, path := range []string{"single", "batch", "best_effort", "no_result"} {
		t.Run(path, func(t *testing.T) {
			for _, value := range []*int{&size, nil} {
				log := &service.UsageLog{
					UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID,
					RequestID: uuid.NewString(), Model: "gpt-5.4", UpstreamTurnStateSizeBytes: value,
				}
				prepared := prepareUsageLogInsert(log)
				switch path {
				case "single":
					_, err := repo.createSingle(ctx, integrationDB, log)
					require.NoError(t, err)
				case "batch":
					_, err := repo.Create(ctx, log)
					require.NoError(t, err)
				case "best_effort":
					query, args := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
					_, err := integrationDB.ExecContext(ctx, query, args...)
					require.NoError(t, err)
				case "no_result":
					require.NoError(t, execUsageLogInsertNoResult(ctx, integrationDB, prepared))
				}
				var stored sql.NullInt64
				require.NoError(t, integrationDB.QueryRowContext(ctx,
					"SELECT id, upstream_turn_state_size_bytes FROM usage_logs WHERE request_id = $1 AND api_key_id = $2",
					log.RequestID, key.ID).Scan(&log.ID, &stored))
				require.Equal(t, value != nil, stored.Valid)
				if value != nil {
					require.Equal(t, int64(*value), stored.Int64)
				}
				got, err := repo.GetByID(ctx, log.ID)
				require.NoError(t, err)
				require.Equal(t, value, got.UpstreamTurnStateSizeBytes)
			}
		})
	}
}
