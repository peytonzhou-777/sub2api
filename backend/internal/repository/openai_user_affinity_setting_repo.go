package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/setting"
)

// CompareAndSet 以完整旧值作为版本令牌，避免多实例管理员更新互相覆盖。
func (r *settingRepository) CompareAndSet(ctx context.Context, key string, expectedValue *string, value string) (bool, error) {
	now := time.Now()
	if expectedValue == nil {
		err := r.client.Setting.Create().SetKey(key).SetValue(value).SetUpdatedAt(now).Exec(ctx)
		if ent.IsConstraintError(err) {
			return false, nil
		}
		return err == nil, err
	}
	affected, err := r.client.Setting.Update().Where(
		setting.KeyEQ(key),
		setting.ValueEQ(*expectedValue),
	).SetValue(value).SetUpdatedAt(now).Save(ctx)
	return affected == 1, err
}
