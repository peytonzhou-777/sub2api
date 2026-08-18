package service

import (
	"context"
	"errors"
	"time"
)

const accountPoolResidentActiveWindow = time.Hour

var ErrAccountPoolResidentStatsUnavailable = errors.New("account pool resident stats unavailable")

// PublicAccountPoolResidents 是号池公开的账号居民数量投影。
type PublicAccountPoolResidents struct {
	Active     int64 `json:"active"`
	Total      int64 `json:"total"`
	Applicable bool  `json:"applicable"`
}

// AccountPoolResidentStats 是仓储按账号聚合的有效居民统计。
type AccountPoolResidentStats struct {
	Active int64
	Total  int64
}

// AccountPoolResidentStatsReader 批量读取账号有效居民数，避免号池构建产生逐账号查询。
type AccountPoolResidentStatsReader interface {
	ListAccountPoolResidentStats(ctx context.Context, accountIDs []int64, activeSince time.Time) (map[int64]AccountPoolResidentStats, error)
}

// SetResidentStatsReader 注入账号居民统计读取器。
func (s *AccountPoolService) SetResidentStatsReader(reader AccountPoolResidentStatsReader) {
	if s == nil {
		return
	}
	s.residentStatsReader = reader
}

// readResidentStats 仅查询适用居民归属的 OpenAI 账号，并固定本批次的一小时活跃窗口。
func (s *AccountPoolService) readResidentStats(ctx context.Context, records []AccountPoolSourceRecord, now time.Time) (map[int64]AccountPoolResidentStats, error) {
	accountIDs := make([]int64, 0, len(records))
	seen := make(map[int64]struct{}, len(records))
	for _, record := range records {
		if record.Platform != PlatformOpenAI {
			continue
		}
		if _, exists := seen[record.ID]; exists {
			continue
		}
		seen[record.ID] = struct{}{}
		accountIDs = append(accountIDs, record.ID)
	}
	if len(accountIDs) == 0 {
		return map[int64]AccountPoolResidentStats{}, nil
	}
	if s == nil || s.residentStatsReader == nil {
		return nil, ErrAccountPoolResidentStatsUnavailable
	}
	return s.residentStatsReader.ListAccountPoolResidentStats(ctx, accountIDs, now.Add(-accountPoolResidentActiveWindow))
}

func applyAccountPoolResidentStats(item *PublicAccountPoolAccount, record AccountPoolSourceRecord, stats map[int64]AccountPoolResidentStats) {
	if item == nil || record.Platform != PlatformOpenAI {
		return
	}
	item.Residents.Applicable = true
	stat := stats[record.ID]
	item.Residents.Active = stat.Active
	item.Residents.Total = stat.Total
}
