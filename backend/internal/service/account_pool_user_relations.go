package service

import (
	"context"
	"errors"
	"fmt"
)

const (
	AccountPoolRelationCurrentResidence  = "current_residence"
	AccountPoolRelationSevenDayContact   = "seven_day_contact"
	AccountPoolRelationHistoricalContact = "historical_contact"
)

var ErrAccountPoolUserRelationUnavailable = errors.New("account pool user relation unavailable")

// AccountPoolUserRelation 是当前登录用户与上游账号的只读关系投影。
type AccountPoolUserRelation struct {
	AccountID           int64
	IsCurrentResidence  bool
	IsSevenDayContact   bool
	IsHistoricalContact bool
}

// AccountPoolUserRelationReader 读取当前用户的有效归属、七日触达和历史触达账号。
type AccountPoolUserRelationReader interface {
	ListAccountPoolUserRelations(ctx context.Context, userID int64) ([]AccountPoolUserRelation, error)
}

// IsAccountPoolRelationFilter 判断用户关系筛选是否属于公开白名单。
func IsAccountPoolRelationFilter(relation string) bool {
	switch relation {
	case AccountPoolRelationCurrentResidence, AccountPoolRelationSevenDayContact, AccountPoolRelationHistoricalContact:
		return true
	default:
		return false
	}
}

// SetUserRelationReader 注入当前用户号池关系读取器，保持公共快照与私有关系解耦。
func (s *AccountPoolService) SetUserRelationReader(reader AccountPoolUserRelationReader) {
	if s == nil {
		return
	}
	s.userRelationReader = reader
}

// ListForUser 在公共号池快照上叠加当前用户关系，并在分页前完成关系筛选。
func (s *AccountPoolService) ListForUser(ctx context.Context, enabledEpoch string, userID int64, page, pageSize int, query AccountPoolListQuery) (*AccountPoolPage, error) {
	if s == nil || s.userRelationReader == nil || userID <= 0 {
		return nil, ErrAccountPoolUserRelationUnavailable
	}
	relations, err := s.userRelationReader.ListAccountPoolUserRelations(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list account pool user relations: %w", err)
	}
	relationByAccount := make(map[int64]AccountPoolUserRelation, len(relations))
	if query.Relation != "" {
		query.RelationAccountIDs = make([]int64, 0, len(relations))
	}
	for _, relation := range relations {
		relationByAccount[relation.AccountID] = relation
		if query.Relation != "" && accountPoolRelationMatches(relation, query.Relation) {
			query.RelationAccountIDs = append(query.RelationAccountIDs, relation.AccountID)
		}
	}
	result, err := s.List(ctx, enabledEpoch, page, pageSize, query)
	if err != nil {
		return nil, err
	}
	for i := range result.Items {
		relation := relationByAccount[result.Items[i].ID]
		result.Items[i].IsCurrentResidence = relation.IsCurrentResidence
		result.Items[i].IsSevenDayContact = relation.IsSevenDayContact
		result.Items[i].IsHistoricalContact = relation.IsHistoricalContact
	}
	return result, nil
}

func accountPoolRelationMatches(relation AccountPoolUserRelation, filter string) bool {
	switch filter {
	case AccountPoolRelationCurrentResidence:
		return relation.IsCurrentResidence
	case AccountPoolRelationSevenDayContact:
		return relation.IsSevenDayContact
	case AccountPoolRelationHistoricalContact:
		return relation.IsHistoricalContact
	default:
		return false
	}
}

func filterAccountPoolItemsByRelation(items []PublicAccountPoolAccount, query AccountPoolListQuery) []PublicAccountPoolAccount {
	if query.Relation == "" {
		return items
	}
	allowed := make(map[int64]struct{}, len(query.RelationAccountIDs))
	for _, accountID := range query.RelationAccountIDs {
		allowed[accountID] = struct{}{}
	}
	filtered := make([]PublicAccountPoolAccount, 0, len(items))
	for _, item := range items {
		if _, ok := allowed[item.ID]; ok {
			filtered = append(filtered, item)
		}
	}
	return filtered
}
