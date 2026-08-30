package repository

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// ListAccountPoolResidentStats 批量统计账号的有效居民总数和指定窗口内的活跃居民数。
func (r *accountRepository) ListAccountPoolResidentStats(ctx context.Context, accountIDs []int64, activeSince time.Time) (map[int64]service.AccountPoolResidentStats, error) {
	stats := make(map[int64]service.AccountPoolResidentStats, len(accountIDs))
	if len(accountIDs) == 0 {
		return stats, nil
	}
	if r == nil || r.sql == nil {
		return nil, errors.New("openai user affinity storage unavailable")
	}
	rows, err := r.sql.QueryContext(ctx, `
		WITH requested AS (
			SELECT UNNEST($1::bigint[]) AS account_id
		), resident_stats AS (
			SELECT account_id,
			       COUNT(DISTINCT user_id) FILTER (
			           WHERE status = 'active' AND expires_at > NOW() AND last_success_at >= $2
			       ) AS active,
			       COUNT(DISTINCT user_id) FILTER (
			           WHERE status = 'active' AND expires_at > NOW()
			       ) AS total,
			       COUNT(DISTINCT id) FILTER (
			           WHERE status IN ('replacement_pending', 'draining', 'reset') AND expires_at > NOW()
			       ) AS draining_slots
			FROM openai_user_resident_slots
			WHERE account_id = ANY($1)
			  AND (scope_key = 'openai' OR scope_key LIKE 'openai:v1:%')
			GROUP BY account_id
		), conversation_stats AS (
			SELECT account_id, COUNT(DISTINCT id) AS active_conversations
			FROM openai_user_conversation_bindings
			WHERE account_id = ANY($1) AND status IN ('provisional', 'active', 'draining')
			  AND active_until > NOW() AND expires_at > NOW()
			GROUP BY account_id
		), contact_stats AS (
			SELECT account_id, COUNT(DISTINCT user_id) AS contacted_users
			FROM account_user_contacts
			WHERE account_id = ANY($1) AND (touch_expires_at > NOW() OR reservation_until > NOW())
			GROUP BY account_id
		)
		SELECT requested.account_id, COALESCE(resident_stats.active, 0), COALESCE(resident_stats.total, 0),
		       COALESCE(resident_stats.draining_slots, 0), COALESCE(conversation_stats.active_conversations, 0),
		       COALESCE(contact_stats.contacted_users, 0)
		FROM requested
		LEFT JOIN resident_stats USING (account_id)
		LEFT JOIN conversation_stats USING (account_id)
		LEFT JOIN contact_stats USING (account_id)
		ORDER BY requested.account_id`, pq.Array(accountIDs), activeSince.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var accountID int64
		var stat service.AccountPoolResidentStats
		if err := rows.Scan(&accountID, &stat.Active, &stat.Total, &stat.DrainingSlots,
			&stat.ActiveConversations, &stat.ContactedUsers); err != nil {
			return nil, err
		}
		stats[accountID] = stat
	}
	return stats, rows.Err()
}

// ListAccountPoolUserRelations 返回当前用户的有效 OpenAI 居住、七日触达和历史触达关系。
func (r *accountRepository) ListAccountPoolUserRelations(ctx context.Context, userID int64) ([]service.AccountPoolUserRelation, error) {
	if r == nil || r.sql == nil {
		return nil, errors.New("openai user affinity storage unavailable")
	}
	rows, err := r.sql.QueryContext(ctx, `
		WITH affinity_config AS (
			SELECT GREATEST(COALESCE((
				SELECT NULLIF((value::jsonb ->> 'resident_ttl_seconds')::double precision, 0)
				FROM settings WHERE key = 'openai_user_affinity_scheduling'
			), 604800), 1) AS resident_ttl_seconds
		), ranked_slots AS (
			SELECT s.account_id,
			       ROW_NUMBER() OVER (
			           PARTITION BY s.user_id, s.scope_key
			           ORDER BY s.usage_score * POWER(0.5,
			               GREATEST(EXTRACT(EPOCH FROM (NOW() - s.score_updated_at)), 0) / c.resident_ttl_seconds) DESC,
			               s.last_success_at DESC NULLS LAST, s.admitted_at, s.account_id
			       ) AS primary_rank
			FROM openai_user_resident_slots s CROSS JOIN affinity_config c
			WHERE s.user_id = $1 AND s.status = 'active' AND s.expires_at > NOW()
			  AND (s.scope_key = 'openai' OR s.scope_key LIKE 'openai:v1:%')
		)
		SELECT account_id,
		       BOOL_OR(is_current_residence) AS is_current_residence,
		       BOOL_OR(is_primary_residence) AS is_primary_residence,
		       BOOL_OR(is_seven_day_contact) AS is_seven_day_contact,
		       BOOL_OR(is_historical_contact) AS is_historical_contact
		FROM (
			SELECT account_id, TRUE AS is_current_residence, primary_rank = 1 AS is_primary_residence,
			       FALSE AS is_seven_day_contact, FALSE AS is_historical_contact
			FROM ranked_slots
			UNION ALL
			SELECT account_id, FALSE AS is_current_residence, FALSE AS is_primary_residence,
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
	defer func() { _ = rows.Close() }()
	relations := make([]service.AccountPoolUserRelation, 0)
	for rows.Next() {
		var relation service.AccountPoolUserRelation
		if err := rows.Scan(&relation.AccountID, &relation.IsCurrentResidence, &relation.IsPrimaryResidence,
			&relation.IsSevenDayContact, &relation.IsHistoricalContact); err != nil {
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

func filterAccountPoolIDsByAllowedIDs(ids []int64, query service.AccountPoolListQuery) []int64 {
	if query.AllowedAccountIDs == nil {
		return ids
	}
	allowed := make(map[int64]struct{}, len(query.AllowedAccountIDs))
	for _, accountID := range query.AllowedAccountIDs {
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

func accountPoolIDAllowed(allowedIDs []int64, accountID int64) bool {
	return allowedIDs == nil || slices.Contains(allowedIDs, accountID)
}

func accountPoolIDInList(ids []int64, accountID int64) bool {
	return slices.Contains(ids, accountID)
}
