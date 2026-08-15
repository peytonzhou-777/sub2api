package repository

import (
	"context"
	"errors"
	"slices"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ListAccountPoolUserRelations 返回当前用户的有效 OpenAI 居住、七日触达和历史触达关系。
func (r *accountRepository) ListAccountPoolUserRelations(ctx context.Context, userID int64) ([]service.AccountPoolUserRelation, error) {
	if r == nil || r.sql == nil {
		return nil, errors.New("openai user affinity storage unavailable")
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT account_id,
		       BOOL_OR(is_current_residence) AS is_current_residence,
		       BOOL_OR(is_seven_day_contact) AS is_seven_day_contact,
		       BOOL_OR(is_historical_contact) AS is_historical_contact
		FROM (
			SELECT account_id, TRUE AS is_current_residence,
			       FALSE AS is_seven_day_contact, FALSE AS is_historical_contact
			FROM user_account_placements
			WHERE user_id = $1 AND status = 'active' AND expires_at > NOW()
			  AND account_id IS NOT NULL
			  AND (scope_key = 'openai' OR scope_key LIKE 'openai:v1:%')
			UNION ALL
			SELECT account_id, FALSE AS is_current_residence,
			       COALESCE(touch_expires_at > NOW(), FALSE) AS is_seven_day_contact,
			       TRUE AS is_historical_contact
			FROM account_user_contacts
			WHERE user_id = $1 AND last_touched_at IS NOT NULL
		) relations
		GROUP BY account_id
		ORDER BY account_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	relations := make([]service.AccountPoolUserRelation, 0)
	for rows.Next() {
		var relation service.AccountPoolUserRelation
		if err := rows.Scan(&relation.AccountID, &relation.IsCurrentResidence, &relation.IsSevenDayContact, &relation.IsHistoricalContact); err != nil {
			return nil, err
		}
		relations = append(relations, relation)
	}
	return relations, rows.Err()
}

func filterAccountPoolIDsByRelation(ids []int64, query service.AccountPoolListQuery) []int64 {
	if query.Relation == "" {
		return ids
	}
	allowed := make(map[int64]struct{}, len(query.RelationAccountIDs))
	for _, accountID := range query.RelationAccountIDs {
		allowed[accountID] = struct{}{}
	}
	filtered := make([]int64, 0, len(ids))
	for _, accountID := range ids {
		if _, ok := allowed[accountID]; ok {
			filtered = append(filtered, accountID)
		}
	}
	return filtered
}

func accountPoolIDInList(ids []int64, accountID int64) bool {
	return slices.Contains(ids, accountID)
}
