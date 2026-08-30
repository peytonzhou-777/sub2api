package service

import (
	"context"
	"fmt"
)

// SetUserAccessReader 注入号池用户可见范围读取器，保持权限投影与快照实现解耦。
func (s *AccountPoolService) SetUserAccessReader(reader AccountPoolUserAccessReader) {
	if s == nil {
		return
	}
	s.userAccessReader = reader
}

// prepareUserAccountPoolQuery 将用户实时可见账号范围注入号池内部查询。
func (s *AccountPoolService) prepareUserAccountPoolQuery(ctx context.Context, userID int64, query AccountPoolListQuery) (AccountPoolUserAccess, AccountPoolListQuery, error) {
	if s == nil || s.userAccessReader == nil || userID <= 0 {
		return AccountPoolUserAccess{}, AccountPoolListQuery{}, ErrAccountPoolUserAccessUnavailable
	}
	access, err := s.userAccessReader.GetAccountPoolUserAccess(ctx, userID, query.GroupID)
	if err != nil {
		return AccountPoolUserAccess{}, AccountPoolListQuery{}, fmt.Errorf("get account pool user access: %w", err)
	}
	if access == nil {
		return AccountPoolUserAccess{}, AccountPoolListQuery{}, ErrAccountPoolUserAccessUnavailable
	}
	query.AllowedAccountIDs = append([]int64{}, access.AccountIDs...)
	return *access, query, nil
}

// GetAccountPoolUserAccess 返回用户可见的启用分组及其当前账号范围。
// 分组可见性沿用模型广场的橱窗语义，不检查订阅有效期。
func (s *APIKeyService) GetAccountPoolUserAccess(ctx context.Context, userID int64, selectedGroupID *int64) (*AccountPoolUserAccess, error) {
	if s == nil || s.userRepo == nil || s.groupRepo == nil || userID <= 0 {
		return nil, ErrAccountPoolUserAccessUnavailable
	}
	allowedGroups, restrictPublicGroups, err := s.GetUserGroupVisibility(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user group visibility: %w", err)
	}
	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active groups: %w", err)
	}

	visibleGroups := make([]AccountPoolGroupOption, 0, len(groups))
	visibleGroupIDs := make([]int64, 0, len(groups))
	selectedVisible := selectedGroupID == nil
	for _, group := range groups {
		if !accountPoolGroupVisible(group, allowedGroups, restrictPublicGroups) {
			continue
		}
		visibleGroups = append(visibleGroups, AccountPoolGroupOption{ID: group.ID, Name: group.Name})
		visibleGroupIDs = append(visibleGroupIDs, group.ID)
		if selectedGroupID != nil && group.ID == *selectedGroupID {
			selectedVisible = true
		}
	}

	if !selectedVisible {
		return &AccountPoolUserAccess{VisibleGroups: visibleGroups, AccountIDs: []int64{}}, nil
	}
	groupIDs := visibleGroupIDs
	if selectedGroupID != nil {
		groupIDs = []int64{*selectedGroupID}
	}
	accountIDs, err := s.groupRepo.GetAccountIDsByGroupIDs(ctx, groupIDs)
	if err != nil {
		return nil, fmt.Errorf("list account pool group accounts: %w", err)
	}
	if accountIDs == nil {
		accountIDs = []int64{}
	}
	return &AccountPoolUserAccess{VisibleGroups: visibleGroups, AccountIDs: accountIDs}, nil
}

func accountPoolGroupVisible(group Group, allowedGroups map[int64]struct{}, restrictPublicGroups bool) bool {
	if group.IsExclusive || restrictPublicGroups {
		_, ok := allowedGroups[group.ID]
		return ok
	}
	return true
}
