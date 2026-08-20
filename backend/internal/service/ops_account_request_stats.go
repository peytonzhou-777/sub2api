package service

import "context"

type opsAccountRequestWindowStatsRepository interface {
	GetAccountRequestWindowStats(ctx context.Context, accountID *int64) ([]*OpsAccountRequestWindowStats, error)
}

// GetAccountRequestWindowStats 返回账号 1/5/30 分钟请求与上游错误指标。
func (s *OpsService) GetAccountRequestWindowStats(ctx context.Context, accountID *int64) ([]*OpsAccountRequestWindowStats, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.opsRepo == nil {
		return []*OpsAccountRequestWindowStats{}, nil
	}
	repo, ok := s.opsRepo.(opsAccountRequestWindowStatsRepository)
	if !ok {
		return []*OpsAccountRequestWindowStats{}, nil
	}
	return repo.GetAccountRequestWindowStats(ctx, accountID)
}
