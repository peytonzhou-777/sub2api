//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestConversationCleanupScript 验证实际交付的 psql 脚本，不用复写的测试 SQL 代替。
func TestConversationCleanupScript(t *testing.T) {
	ctx := context.Background()
	f := newOpenAIReservationFixture(t)
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET platform='openai' WHERE id=$1`, f.groupID)
	require.NoError(t, err)
	var previous sql.NullString
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT (SELECT value FROM settings WHERE key='openai_user_affinity_scheduling')`).Scan(&previous))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('openai_user_affinity_scheduling','{"conversation_active_ttl_seconds":2400}') ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`)
	require.NoError(t, err)
	t.Cleanup(func() {
		if previous.Valid {
			_, _ = integrationDB.ExecContext(context.Background(), `UPDATE settings SET value=$1 WHERE key='openai_user_affinity_scheduling'`, previous.String)
		} else {
			_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM settings WHERE key='openai_user_affinity_scheduling'`)
		}
	})
	cfg := service.DefaultOpenAIUserAffinityConfig()
	cfg.ConversationActiveTTLSeconds = 2400
	cfg.ResidentTTLSeconds = 259200
	scope := fmt.Sprintf("openai:v1:group:%d:lane:endpoint:responses", f.groupID)
	seed := func(hash string) service.OpenAIUserConversationTransition {
		token := uuid.NewString()
		b, created, e := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{UserID: f.userID, APIKeyID: f.apiKeyID, AccountID: f.account.ID, ScopeKey: scope, ConversationHash: strings.Repeat(hash, 64), PlacementGeneration: 1, ProvisionalToken: token, ContextRebuildable: true, Config: cfg, Aliases: []service.OpenAIUserConversationAlias{{ScopeKey: scope, Type: "codex_thread", Hash: strings.Repeat(hash, 64)}}})
		require.NoError(t, e)
		require.True(t, created)
		tr := service.OpenAIUserConversationTransition{BindingID: b.ID, UserID: f.userID, APIKeyID: f.apiKeyID, AccountID: f.account.ID, ScopeKey: scope, ConversationHash: b.ConversationHash, ResidentSlotID: b.ResidentSlotID, SlotGeneration: b.SlotGeneration, BindingEpoch: service.OpenAIConversationBindingEpoch, ProvisionalToken: token, Config: cfg}
		_, e = repo.CommitOpenAIUserConversationBinding(ctx, tr)
		require.NoError(t, e)
		tr.ProvisionalToken = ""
		return tr
	}
	stale := seed("a")
	live := seed("b")
	var residentExpiry time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT expires_at FROM openai_user_resident_slots WHERE id=$1`, stale.ResidentSlotID).Scan(&residentExpiry))
	hold := uuid.NewString()
	held, err := repo.AcquireOpenAIConversationActivity(ctx, stale, hold, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, held)
	_, err = integrationDB.ExecContext(ctx, `UPDATE openai_user_conversation_bindings SET active_until=NOW()-INTERVAL '1 hour',last_success_at=NOW()-INTERVAL '2 hours',expires_at=NOW()+INTERVAL '3 days' WHERE id=$1`, stale.BindingID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE openai_user_conversation_bindings SET expires_at=NOW()+INTERVAL '3 days' WHERE id=$1`, live.BindingID)
	require.NoError(t, err)
	script, err := os.ReadFile("../../../deploy/sql/expire_openai_conversations.sql")
	require.NoError(t, err)
	run := func(apply, confirm bool) error {
		command := exec.Command("docker", "exec", "-i", integrationPostgresContainerID, "psql", "-U", "postgres", "-d", "sub2api_test", "-v", fmt.Sprintf("group_id=%d", f.groupID), "-v", fmt.Sprintf("apply=%t", apply), "-v", fmt.Sprintf("maintenance_confirmed=%t", confirm))
		command.Stdin = strings.NewReader(string(script))
		out, e := command.CombinedOutput()
		if e != nil {
			t.Log(string(out))
		}
		return e
	}
	require.NoError(t, run(false, false))
	require.Error(t, run(true, false))
	require.Error(t, run(true, true), "未排空时不能写入")
	require.NoError(t, repo.ReleaseOpenAIConversationActivity(ctx, hold))
	require.NoError(t, run(true, true))
	require.NoError(t, run(true, true), "清理应可重复执行")
	var staleState string
	var remainingAliases int
	var afterResident time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM openai_user_conversation_bindings WHERE id=$1`, stale.BindingID).Scan(&staleState))
	require.Equal(t, "expired", staleState)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_user_conversation_aliases WHERE binding_id=$1 AND expires_at>NOW()`, stale.BindingID).Scan(&remainingAliases))
	require.Zero(t, remainingAliases)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT expires_at FROM openai_user_resident_slots WHERE id=$1`, stale.ResidentSlotID).Scan(&afterResident))
	require.Equal(t, residentExpiry, afterResident)
	b, err := repo.GetOpenAIUserConversationBinding(ctx, live.UserID, live.APIKeyID, live.ScopeKey, live.ConversationHash)
	require.NoError(t, err)
	require.NotNil(t, b)
	require.WithinDuration(t, *b.ActiveUntil, b.ExpiresAt, time.Millisecond)
}
