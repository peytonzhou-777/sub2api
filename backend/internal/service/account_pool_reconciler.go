package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

const accountPoolHeartbeatJobName = "account_pool_reconciliation"

type AccountPoolReconciler struct {
	pool     *AccountPoolService
	settings *SettingService
	ops      OpsRepository
	interval time.Duration
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

func NewAccountPoolReconciler(pool *AccountPoolService, settings *SettingService, ops OpsRepository, interval time.Duration) *AccountPoolReconciler {
	if interval < 30*time.Second {
		interval = 60 * time.Second
	}
	return &AccountPoolReconciler{pool: pool, settings: settings, ops: ops, interval: interval, stop: make(chan struct{}), done: make(chan struct{})}
}

// Start 启动号池周期对账；开关关闭时仅等待下一周期，不生产快照。
func (r *AccountPoolReconciler) Start() {
	if r == nil {
		return
	}
	r.once.Do(func() { go r.run() })
}

func (r *AccountPoolReconciler) run() {
	defer close(r.done)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.reconcileOnce()
	for {
		select {
		case <-ticker.C:
			r.reconcileOnce()
		case <-r.stop:
			return
		}
	}
}

func (r *AccountPoolReconciler) reconcileOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), r.interval)
	defer cancel()
	if r.settings == nil || !r.settings.IsAccountPoolEnabled(ctx) {
		return
	}
	enabledEpoch, err := r.settings.EnsureAccountPoolEnabledEpoch(ctx)
	if err != nil {
		slog.Error("account_pool.enabled_epoch_failed", "error", err)
		return
	}
	startedAt := time.Now().UTC()
	generation := strconv.FormatInt(startedAt.UnixNano(), 36)
	err = r.pool.Reconcile(ctx, generation, enabledEpoch)
	duration := time.Since(startedAt)
	if err != nil {
		if errors.Is(err, ErrAccountPoolBuildLockNotAcquired) {
			slog.Debug("account_pool.reconciliation_skipped", "generation", generation, "reason", "build_lock_not_acquired")
			return
		}
		slog.Error("account_pool.reconciliation_failed", "generation", generation, "duration_ms", duration.Milliseconds(), "error", err)
		r.recordHeartbeat(startedAt, duration, err, "")
		return
	}
	result := fmt.Sprintf("generation=%s", generation)
	r.recordHeartbeat(startedAt, duration, nil, result)
}

func (r *AccountPoolReconciler) recordHeartbeat(runAt time.Time, duration time.Duration, runErr error, result string) {
	if r.ops == nil {
		return
	}
	durationMS := duration.Milliseconds()
	input := &OpsUpsertJobHeartbeatInput{JobName: accountPoolHeartbeatJobName, LastRunAt: &runAt, LastDurationMs: &durationMS}
	if runErr != nil {
		message := "account pool reconciliation failed"
		input.LastErrorAt, input.LastError = &runAt, &message
	} else {
		input.LastSuccessAt, input.LastResult = &runAt, &result
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = r.ops.UpsertJobHeartbeat(ctx, input)
}

// Stop 停止后台对账并等待退出。
func (r *AccountPoolReconciler) Stop() {
	if r == nil {
		return
	}
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
	select {
	case <-r.done:
	case <-time.After(3 * time.Second):
	}
}
