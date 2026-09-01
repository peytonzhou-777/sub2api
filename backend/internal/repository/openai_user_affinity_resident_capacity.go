package repository

import (
	"context"
	"time"
)

// getOpenAIAccountResidentCapacity 按账号下唯一用户统计当前仍占用的居住关系。
// placement 是单槽兼容投影，slot 是多槽权威状态；UNION 保证跨 scope 和双写记录只计一次。
func getOpenAIAccountResidentCapacity(
	ctx context.Context,
	exec sqlExecutor,
	accountID, userID int64,
	now time.Time,
) (int, bool, error) {
	var residentUsers int
	var userAlreadyResident bool
	err := scanSingleRow(ctx, exec, `
		WITH resident_users AS (
			SELECT user_id
			FROM openai_user_resident_slots
			WHERE account_id = $1
			  AND (status = 'draining' OR (
			      status IN ('provisional', 'active', 'replacement_pending') AND expires_at > $2
			  ))
			UNION
			SELECT user_id
			FROM user_account_placements
			WHERE account_id = $1 AND status = 'active' AND expires_at > $2
		)
		SELECT COUNT(*), COALESCE(BOOL_OR(user_id = $3), FALSE)
		FROM resident_users`, []any{accountID, now.UTC(), userID}, &residentUsers, &userAlreadyResident)
	return residentUsers, userAlreadyResident, err
}
