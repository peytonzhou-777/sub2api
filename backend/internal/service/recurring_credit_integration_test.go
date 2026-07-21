//go:build integration

package service

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const recurringCreditPostgresImage = "postgres:18.1-alpine3.23"

func openRecurringCreditPostgres(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		if os.Getenv("CI") != "" {
			require.NoError(t, err, "CI 环境必须提供 Docker")
		}
		t.Skip("Docker 不可用，跳过 PostgreSQL 集成测试")
	}

	container, err := tcpostgres.Run(
		ctx,
		recurringCreditPostgresImage,
		tcpostgres.WithDatabase("sub2api_test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		require.NoError(t, container.Terminate(cleanupCtx))
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})
	require.NoError(t, db.PingContext(ctx))
	return ctx, db
}

// TestRollingActivityQueriesAcceptTimeParameterInPostgres 使用真实 PostgreSQL 验证统计和快照 SQL 的时间参数类型。
func TestRollingActivityQueriesAcceptTimeParameterInPostgres(t *testing.T) {
	ctx, db := openRecurringCreditPostgres(t)

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, tx.Rollback())
	})

	_, err = tx.ExecContext(ctx, `
		CREATE TEMP TABLE users (
			id BIGINT PRIMARY KEY,
			email TEXT,
			username TEXT,
			status TEXT NOT NULL,
			deleted_at TIMESTAMPTZ,
			last_active_at TIMESTAMPTZ
		);
		CREATE TEMP TABLE api_keys (
			user_id BIGINT NOT NULL,
			last_used_at TIMESTAMPTZ
		);
		CREATE TEMP TABLE recurring_credit_user_items (
			batch_id BIGINT NOT NULL,
			user_id BIGINT NOT NULL,
			email TEXT NOT NULL,
			username TEXT NOT NULL,
			user_status TEXT NOT NULL,
			user_deleted BOOLEAN NOT NULL,
			actual_cost NUMERIC NOT NULL,
			net_recharge NUMERIC NOT NULL,
			api_last_used_at TIMESTAMPTZ,
			site_last_active_at TIMESTAMPTZ,
			qualification_reason TEXT NOT NULL,
			grant_amount NUMERIC NOT NULL,
			result TEXT NOT NULL,
			exclusion_reason TEXT NOT NULL,
			UNIQUE (batch_id, user_id)
		)
	`)
	require.NoError(t, err)

	cutoff := time.Date(2026, 7, 26, 14, 30, 38, 0, time.UTC)
	lastActivity := cutoff.Add(-time.Hour)
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO users(id,email,username,status,last_active_at) VALUES($1,$2,$3,$4,$5)`,
		int64(1),
		"active@example.com",
		"active-user",
		"active",
		lastActivity,
	)
	require.NoError(t, err)
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO api_keys(user_id,last_used_at) VALUES($1,$2)`,
		int64(1),
		lastActivity,
	)
	require.NoError(t, err)

	var eligibleCount, apiCount, siteCount, bothCount int
	err = tx.QueryRowContext(ctx, rollingActivityStatsSQL, cutoff).Scan(
		&eligibleCount,
		&apiCount,
		&siteCount,
		&bothCount,
	)
	require.NoError(t, err)
	require.Equal(t, []int{1, 1, 1, 1}, []int{eligibleCount, apiCount, siteCount, bothCount})

	result, err := tx.ExecContext(ctx, rollingActivitySnapshotSQL, cutoff, int64(42))
	require.NoError(t, err)
	affected, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, affected)

	var reason string
	var apiLastUsedAt, siteLastActiveAt time.Time
	err = tx.QueryRowContext(
		ctx,
		`SELECT qualification_reason,api_last_used_at,site_last_active_at
		 FROM recurring_credit_user_items
		 WHERE batch_id=$1 AND user_id=$2`,
		int64(42),
		int64(1),
	).Scan(&reason, &apiLastUsedAt, &siteLastActiveAt)
	require.NoError(t, err)
	require.Equal(t, "api_and_site_activity", reason)
	require.True(t, apiLastUsedAt.Equal(lastActivity))
	require.True(t, siteLastActiveAt.Equal(lastActivity))
}

// TestRecurringCreditReissuePreservesSnapshotAndExpiryInPostgres 覆盖补发预览、快照复用、执行和任务状态隔离。
func TestRecurringCreditReissuePreservesSnapshotAndExpiryInPostgres(t *testing.T) {
	ctx, db := openRecurringCreditPostgres(t)
	_, err := db.ExecContext(ctx, `
		CREATE TABLE users (
			id BIGINT PRIMARY KEY,
			email TEXT NOT NULL DEFAULT '',
			username TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			deleted_at TIMESTAMPTZ,
			last_active_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE api_keys (
			user_id BIGINT NOT NULL,
			last_used_at TIMESTAMPTZ
		);
		CREATE TABLE recurring_credit_tasks (
			id BIGINT PRIMARY KEY,
			remaining_runs INTEGER,
			status TEXT NOT NULL,
			next_run_at TIMESTAMPTZ,
			execution_mode TEXT NOT NULL,
			version INTEGER NOT NULL DEFAULT 1,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE recurring_credit_batches (
			id BIGSERIAL PRIMARY KEY,
			task_id BIGINT NOT NULL,
			task_name TEXT NOT NULL,
			scheduled_at TIMESTAMPTZ NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL,
			qualification_start TIMESTAMPTZ NOT NULL,
			qualification_end TIMESTAMPTZ NOT NULL,
			qualification_cutoff_at TIMESTAMPTZ,
			config_version INTEGER NOT NULL,
			eligibility_policy TEXT NOT NULL,
			validity_days INTEGER,
			schedule_type TEXT NOT NULL,
			day_of_month INTEGER,
			day_of_week INTEGER,
			local_time TEXT NOT NULL,
			timezone TEXT NOT NULL,
			amount NUMERIC(20,8) NOT NULL,
			execution_mode TEXT NOT NULL,
			status TEXT NOT NULL,
			claimed_at TIMESTAMPTZ,
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_expires_at TIMESTAMPTZ,
			heartbeat_at TIMESTAMPTZ,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			eligible_user_count INTEGER NOT NULL DEFAULT 0,
			issued_user_count INTEGER NOT NULL DEFAULT 0,
			excluded_user_count INTEGER NOT NULL DEFAULT 0,
			usage_eligible_count INTEGER NOT NULL DEFAULT 0,
			recharge_eligible_count INTEGER NOT NULL DEFAULT 0,
			api_active_count INTEGER NOT NULL DEFAULT 0,
			site_active_count INTEGER NOT NULL DEFAULT 0,
			both_active_count INTEGER NOT NULL DEFAULT 0,
			snapshot_completed_at TIMESTAMPTZ,
			issued_amount NUMERIC(20,8) NOT NULL DEFAULT 0,
			failure_code TEXT NOT NULL DEFAULT '',
			failure_message TEXT NOT NULL DEFAULT '',
			finished_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE user_limited_credit_grants (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT NOT NULL,
			source_type TEXT NOT NULL,
			source_id BIGINT,
			initial_amount NUMERIC(20,8) NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL,
			status TEXT NOT NULL,
			notes TEXT NOT NULL
		);
		CREATE TABLE recurring_credit_user_items (
			id BIGSERIAL PRIMARY KEY,
			batch_id BIGINT NOT NULL,
			user_id BIGINT NOT NULL,
			email TEXT NOT NULL,
			username TEXT NOT NULL,
			user_status TEXT NOT NULL,
			user_deleted BOOLEAN NOT NULL,
			actual_cost NUMERIC(20,8) NOT NULL,
			net_recharge NUMERIC(20,8) NOT NULL,
			api_last_used_at TIMESTAMPTZ,
			site_last_active_at TIMESTAMPTZ,
			qualification_reason TEXT NOT NULL,
			grant_amount NUMERIC(20,8) NOT NULL,
			grant_id BIGINT,
			result TEXT NOT NULL,
			exclusion_reason TEXT NOT NULL,
			UNIQUE(batch_id,user_id)
		);
		CREATE TABLE user_limited_credit_ledger (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT NOT NULL,
			grant_id BIGINT NOT NULL,
			event_type TEXT NOT NULL,
			amount NUMERIC(20,8) NOT NULL,
			batch_id TEXT NOT NULL,
			notes TEXT NOT NULL
		);
		CREATE TABLE recurring_credit_task_audits (
			id BIGSERIAL PRIMARY KEY,
			task_id BIGINT NOT NULL,
			admin_id BIGINT NOT NULL,
			admin_email TEXT NOT NULL,
			client_ip TEXT NOT NULL,
			action TEXT NOT NULL,
			before_snapshot JSONB,
			after_snapshot JSONB,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	require.NoError(t, err)
	reissueMigration, err := migrations.FS.ReadFile("187_recurring_credit_reissue.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(reissueMigration))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(reissueMigration))
	require.NoError(t, err, "补发迁移必须可重复执行")

	now := time.Now().UTC().Truncate(time.Second)
	cutoff := now.Add(-time.Hour)
	windowStart := cutoff.Add(-30 * 24 * time.Hour)
	expiresAt := cutoff.Add(7 * 24 * time.Hour)
	nextRunAt := now.Add(7 * 24 * time.Hour)
	historicalActivity := cutoff.Add(-2 * time.Hour)
	_, err = db.ExecContext(ctx, `INSERT INTO users(id,email,username,status,last_active_at,created_at) VALUES
		(1,'eligible@example.com','eligible','active',$1,$2),
		(99,'admin@example.com','admin','active',NULL,$2)`, historicalActivity, windowStart)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO api_keys(user_id,last_used_at) VALUES(1,$1)`, now)
	require.NoError(t, err, "当前 last_used_at 已超出原窗口，用于证明补发复用已保存快照")
	_, err = db.ExecContext(ctx, `INSERT INTO recurring_credit_tasks(id,remaining_runs,status,next_run_at,execution_mode) VALUES(10,5,'active',$1,'finite')`, nextRunAt)
	require.NoError(t, err)

	var sourceBatchID int64
	err = db.QueryRowContext(ctx, `INSERT INTO recurring_credit_batches(
		task_id,task_name,scheduled_at,expires_at,qualification_start,qualification_end,qualification_cutoff_at,
		config_version,eligibility_policy,schedule_type,day_of_week,local_time,timezone,amount,execution_mode,status,
		eligible_user_count,api_active_count,snapshot_completed_at,failure_code,failure_message,finished_at)
		VALUES(10,'每周重置额度',$1,$2,$3,$4,$4,3,'rolling_30d_activity_v1','weekly',1,'08:00','Asia/Shanghai',12.5,'finite','failed',
			1,1,$4,'EXECUTION_FAILED','test failure',$5)
		RETURNING id`, cutoff, expiresAt, windowStart, cutoff, now).Scan(&sourceBatchID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO recurring_credit_user_items(
		batch_id,user_id,email,username,user_status,user_deleted,actual_cost,net_recharge,
		api_last_used_at,site_last_active_at,qualification_reason,grant_amount,result,exclusion_reason)
		VALUES($1,1,'eligible@example.com','eligible','active',FALSE,0,0,$2,NULL,'api_activity',0,'pending','')`,
		sourceBatchID, historicalActivity)
	require.NoError(t, err)

	recurringService := &RecurringCreditService{db: db, defaultTimezone: "Asia/Shanghai", wakeCh: make(chan struct{}, 1)}
	preview, err := recurringService.PreviewReissue(ctx, 10, sourceBatchID)
	require.NoError(t, err)
	require.Equal(t, sourceBatchID, preview.SourceBatchID)
	require.NotNil(t, preview.SnapshotSourceBatchID)
	require.Equal(t, sourceBatchID, *preview.SnapshotSourceBatchID)
	require.Equal(t, 1, preview.ReferenceEligibleCount)
	require.Equal(t, 12.5, preview.EstimatedTotal)
	require.True(t, preview.ExpiresAt.Equal(expiresAt))
	require.True(t, preview.QualificationStart.Equal(windowStart))
	require.True(t, preview.QualificationEnd.Equal(cutoff))

	reissue, err := recurringService.ReissueBatch(ctx, 10, sourceBatchID, RecurringCreditActor{AdminID: 99, IP: "127.0.0.1"})
	require.NoError(t, err)
	require.NotNil(t, reissue.ReissueOfBatchID)
	require.Equal(t, sourceBatchID, *reissue.ReissueOfBatchID)
	require.Equal(t, "running", reissue.Status)
	require.True(t, reissue.ExpiresAt.Equal(expiresAt))
	require.False(t, reissue.CanReissue)

	var copiedActivity time.Time
	err = db.QueryRowContext(ctx, `SELECT api_last_used_at FROM recurring_credit_user_items WHERE batch_id=$1 AND user_id=1`, reissue.ID).Scan(&copiedActivity)
	require.NoError(t, err)
	require.True(t, copiedActivity.Equal(historicalActivity))

	_, err = recurringService.ReissueBatch(ctx, 10, sourceBatchID, RecurringCreditActor{AdminID: 99})
	require.Error(t, err, "同一原始批次已有执行中补发时必须拒绝重复补发")

	runner := &RecurringCreditRunner{service: recurringService, db: db, owner: "integration-test"}
	runner.takeOverStale(ctx)
	require.Eventually(t, func() bool {
		var status string
		queryErr := db.QueryRowContext(ctx, `SELECT status FROM recurring_credit_batches WHERE id=$1`, reissue.ID).Scan(&status)
		return queryErr == nil && status == "succeeded"
	}, 10*time.Second, 50*time.Millisecond, "补发批次应被现有执行器立即领取并完成")
	runner.wg.Wait()

	var batchStatus string
	var grantExpiresAt time.Time
	err = db.QueryRowContext(ctx, `SELECT b.status,g.expires_at
		FROM recurring_credit_batches b
		JOIN recurring_credit_user_items i ON i.batch_id=b.id
		JOIN user_limited_credit_grants g ON g.id=i.grant_id
		WHERE b.id=$1 AND i.user_id=1`, reissue.ID).Scan(&batchStatus, &grantExpiresAt)
	require.NoError(t, err)
	require.Equal(t, "succeeded", batchStatus)
	require.True(t, grantExpiresAt.Equal(expiresAt))

	var remainingRuns int
	var actualNextRun time.Time
	err = db.QueryRowContext(ctx, `SELECT remaining_runs,next_run_at FROM recurring_credit_tasks WHERE id=10`).Scan(&remainingRuns, &actualNextRun)
	require.NoError(t, err)
	require.Equal(t, 5, remainingRuns)
	require.True(t, actualNextRun.Equal(nextRunAt))

	var auditCount int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recurring_credit_task_audits WHERE task_id=10 AND action='reissue_batch'`).Scan(&auditCount)
	require.NoError(t, err)
	require.Equal(t, 1, auditCount)
}
