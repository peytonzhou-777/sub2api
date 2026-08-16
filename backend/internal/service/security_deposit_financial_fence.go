package service

import (
	"context"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// securityDepositFinancialFence 复用账户退款已有的分布式栅栏和并发排空能力。
// 保证金退款、管理员扣除和其他资金减少动作必须共享这一接缝。
type securityDepositFinancialFence interface {
	RefundBillingFence
	GetUserConcurrency(ctx context.Context, userID int64) (int, error)
	GetUserWaitingCount(ctx context.Context, userID int64) (int, error)
}

// withSecurityDepositFinancialFence 阻断新请求并等待在途/排队请求归零后执行资金减少事务。
func (s *SecurityDepositService) withSecurityDepositFinancialFence(ctx context.Context, userID int64, operationID string, fn func() error) error {
	fence, ok := s.authCacheInvalidator.(securityDepositFinancialFence)
	if !ok || fence == nil {
		return infraerrors.ServiceUnavailable("REFUND_BILLING_FENCE_UNAVAILABLE", "security deposit financial fence is unavailable")
	}
	if err := fence.AcquireRefundBillingLock(ctx, userID, operationID); err != nil {
		return infraerrors.ServiceUnavailable("REFUND_BILLING_FENCE_UNAVAILABLE", "cannot establish security deposit financial fence").WithCause(err)
	}
	defer func() { _ = fence.ReleaseRefundBillingLock(context.Background(), userID, operationID) }()
	s.invalidateSecurityDepositUser(ctx, userID)

	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		active, err := fence.GetUserConcurrency(ctx, userID)
		if err != nil {
			return infraerrors.ServiceUnavailable("REFUND_DRAIN_UNAVAILABLE", "cannot verify active API requests").WithCause(err)
		}
		waiting, err := fence.GetUserWaitingCount(ctx, userID)
		if err != nil {
			return infraerrors.ServiceUnavailable("REFUND_DRAIN_UNAVAILABLE", "cannot verify queued API requests").WithCause(err)
		}
		if active == 0 && waiting == 0 {
			return fn()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return infraerrors.Conflict("SECURITY_DEPOSIT_DRAIN_TIMEOUT", "active API requests did not drain before the security deposit operation")
		case <-ticker.C:
		}
	}
}
