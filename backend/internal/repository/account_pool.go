package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	accountPoolKeyPrefix              = "account_pool:v1:"
	accountPoolCurrentKey             = accountPoolKeyPrefix + "current"
	accountPoolLockKey                = accountPoolKeyPrefix + "build_lock"
	accountPoolPersonalUsageKeyPrefix = accountPoolKeyPrefix + "personal_usage:v2:"
)

type accountPoolSource struct{ client *dbent.Client }

// NewAccountPoolSource 创建只选择号池构建与展示投影所需字段的窄数据库读接口；凭据字段仅在内存中提取白名单展示值。
func NewAccountPoolSource(client *dbent.Client) service.AccountPoolSource {
	return &accountPoolSource{client: client}
}

func (r *accountPoolSource) ListAccountPoolBuildBatch(ctx context.Context, afterID int64, limit int) ([]service.AccountPoolSourceRecord, bool, error) {
	if limit <= 0 {
		limit = 500
	}
	// 号池只公开至少属于一个分组的账号；未分组账号不进入构建快照。
	query := r.client.Account.Query().Where(
		dbaccount.IDGT(afterID),
		dbaccount.HasAccountGroups(),
	)
	accounts, err := selectAccountPoolFields(query).
		Order(dbent.Asc(dbaccount.FieldID)).
		Limit(limit + 1).
		All(ctx)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(accounts) > limit
	if hasMore {
		accounts = accounts[:limit]
	}
	records := accountPoolSourceRecords(accounts)
	if err := r.attachParentPresentation(ctx, records); err != nil {
		return nil, false, err
	}
	return records, hasMore, nil
}

func (r *accountPoolSource) ListAccountPoolPage(ctx context.Context, page, pageSize int, accountID *int64, sortOrder string) ([]service.AccountPoolSourceRecord, int64, error) {
	// 与构建路径保持一致，数据库降级也不得返回未分组账号。
	query := r.client.Account.Query().Where(dbaccount.HasAccountGroups())
	if accountID != nil {
		query = query.Where(dbaccount.IDEQ(*accountID))
		page = 1
	}
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	order := dbent.Asc(dbaccount.FieldID)
	if sortOrder == service.AccountPoolSortDesc {
		order = dbent.Desc(dbaccount.FieldID)
	}
	accounts, err := selectAccountPoolFields(query).
		Order(order).
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, 0, err
	}
	records := accountPoolSourceRecords(accounts)
	if err := r.attachParentPresentation(ctx, records); err != nil {
		return nil, 0, err
	}
	return records, int64(total), nil
}

// attachParentPresentation 为影子账号补充管理员页使用的母账号展示来源；不向快照写入凭据。
func (r *accountPoolSource) attachParentPresentation(ctx context.Context, records []service.AccountPoolSourceRecord) error {
	parentIDs := make([]int64, 0)
	seen := make(map[int64]struct{})
	for _, record := range records {
		if record.ParentAccountID == nil {
			continue
		}
		if _, exists := seen[*record.ParentAccountID]; exists {
			continue
		}
		seen[*record.ParentAccountID] = struct{}{}
		parentIDs = append(parentIDs, *record.ParentAccountID)
	}
	if len(parentIDs) == 0 {
		return nil
	}
	parents, err := r.client.Account.Query().
		Where(dbaccount.IDIn(parentIDs...)).
		Select(dbaccount.FieldID, dbaccount.FieldCredentials, dbaccount.FieldExtra).
		All(ctx)
	if err != nil {
		return fmt.Errorf("list account pool parent presentation: %w", err)
	}
	byID := make(map[int64]*dbent.Account, len(parents))
	for _, parent := range parents {
		byID[parent.ID] = parent
	}
	for i := range records {
		if records[i].ParentAccountID == nil {
			continue
		}
		parent := byID[*records[i].ParentAccountID]
		if parent == nil {
			continue
		}
		records[i].ParentCredentials = parent.Credentials
		records[i].ParentExtra = parent.Extra
	}
	return nil
}

func selectAccountPoolFields(query *dbent.AccountQuery) *dbent.AccountSelect {
	return query.Select(
		dbaccount.FieldID,
		dbaccount.FieldPlatform,
		dbaccount.FieldType,
		dbaccount.FieldConcurrency,
		dbaccount.FieldStatus,
		dbaccount.FieldSchedulable,
		dbaccount.FieldRateLimitResetAt,
		dbaccount.FieldOverloadUntil,
		dbaccount.FieldTempUnschedulableUntil,
		dbaccount.FieldSessionWindowEnd,
		dbaccount.FieldParentAccountID,
		dbaccount.FieldQuotaDimension,
		dbaccount.FieldCredentials,
		dbaccount.FieldExtra,
	)
}

func accountPoolSourceRecords(accounts []*dbent.Account) []service.AccountPoolSourceRecord {
	records := make([]service.AccountPoolSourceRecord, 0, len(accounts))
	for _, account := range accounts {
		records = append(records, service.AccountPoolSourceRecord{
			ID: account.ID, Platform: account.Platform, Type: account.Type,
			Concurrency: account.Concurrency, Status: account.Status, Schedulable: account.Schedulable,
			RateLimitResetAt: account.RateLimitResetAt, OverloadUntil: account.OverloadUntil,
			TempUnschedulableUntil: account.TempUnschedulableUntil, SessionWindowEnd: account.SessionWindowEnd,
			ParentAccountID: account.ParentAccountID, QuotaDimension: string(account.QuotaDimension), Extra: account.Extra,
			Credentials: account.Credentials,
		})
	}
	return records
}

type accountPoolSnapshotCache struct{ rdb *redis.Client }

type accountPoolGenerationMeta struct {
	SchemaVersion    int                `json:"schema_version"`
	Generation       string             `json:"generation"`
	EnabledEpoch     string             `json:"enabled_epoch"`
	AccountIDs       []int64            `json:"account_ids"`
	StatusAccountIDs map[string][]int64 `json:"status_account_ids"`
}

// NewAccountPoolSnapshotCache 创建独立版本键空间的公开快照缓存。
func NewAccountPoolSnapshotCache(rdb *redis.Client) service.AccountPoolSnapshotCache {
	return &accountPoolSnapshotCache{rdb: rdb}
}

// GetAccountPoolPersonalUsage 读取用户私有的短期用量缓存；未命中不视为错误。
func (c *accountPoolSnapshotCache) GetAccountPoolPersonalUsage(ctx context.Context, key string) (*service.AccountPoolPersonalUsage, bool, error) {
	payload, err := c.rdb.Get(ctx, accountPoolPersonalUsageKey(key)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var value service.AccountPoolPersonalUsage
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return nil, false, err
	}
	return &value, true, nil
}

// SetAccountPoolPersonalUsage 写入用户私有的短期用量缓存，不进入公开 generation。
func (c *accountPoolSnapshotCache) SetAccountPoolPersonalUsage(ctx context.Context, key string, value *service.AccountPoolPersonalUsage, ttl time.Duration) error {
	if value == nil || ttl <= 0 {
		return nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, accountPoolPersonalUsageKey(key), payload, ttl).Err()
}

func (c *accountPoolSnapshotCache) AcquireAccountPoolBuildLock(ctx context.Context, owner string, ttl time.Duration) (bool, error) {
	return c.rdb.SetNX(ctx, accountPoolLockKey, owner, ttl).Result()
}

func (c *accountPoolSnapshotCache) RenewAccountPoolBuildLock(ctx context.Context, owner string, ttl time.Duration) (bool, error) {
	const renewScript = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("PEXPIRE", KEYS[1], ARGV[2]) end return 0`
	result, err := c.rdb.Eval(ctx, renewScript, []string{accountPoolLockKey}, owner, ttl.Milliseconds()).Int64()
	return result == 1, err
}

func (c *accountPoolSnapshotCache) ReleaseAccountPoolBuildLock(ctx context.Context, owner string) error {
	const releaseScript = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) end return 0`
	return c.rdb.Eval(ctx, releaseScript, []string{accountPoolLockKey}, owner).Err()
}

// ReadAccountPoolPreviousCapacities 只读取同一启用代次中上一份完整快照的可信容量观测。
func (c *accountPoolSnapshotCache) ReadAccountPoolPreviousCapacities(ctx context.Context, enabledEpoch string, accountIDs []int64) (map[int64]service.PublicAccountPoolCapacity, error) {
	generation, err := c.rdb.Get(ctx, accountPoolCurrentKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return map[int64]service.PublicAccountPoolCapacity{}, service.ErrAccountPoolSnapshotNotReady
		}
		return nil, service.ErrAccountPoolSnapshotUnavailable
	}
	values, err := c.rdb.MGet(ctx, accountPoolMetaKey(generation), accountPoolCompleteKey(generation)).Result()
	if err != nil {
		return nil, service.ErrAccountPoolSnapshotUnavailable
	}
	if len(values) != 2 || values[0] == nil || values[1] != "1" {
		return nil, service.ErrAccountPoolSnapshotNotReady
	}
	var meta accountPoolGenerationMeta
	if err := json.Unmarshal([]byte(fmt.Sprint(values[0])), &meta); err != nil || meta.SchemaVersion != service.AccountPoolSchemaVersion || meta.Generation != generation || meta.EnabledEpoch != enabledEpoch {
		return nil, service.ErrAccountPoolSnapshotNotReady
	}

	keys := make([]string, 0, len(accountIDs))
	ids := make([]int64, 0, len(accountIDs))
	for _, id := range accountIDs {
		idx := sort.Search(len(meta.AccountIDs), func(i int) bool { return meta.AccountIDs[i] >= id })
		if idx < len(meta.AccountIDs) && meta.AccountIDs[idx] == id {
			ids = append(ids, id)
			keys = append(keys, accountPoolItemKey(generation, id))
		}
	}
	result := make(map[int64]service.PublicAccountPoolCapacity, len(keys))
	if len(keys) == 0 {
		return result, nil
	}
	payloads, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, service.ErrAccountPoolSnapshotUnavailable
	}
	for i, payload := range payloads {
		if payload == nil {
			return nil, service.ErrAccountPoolSnapshotUnavailable
		}
		var item service.PublicAccountPoolAccount
		if err := json.Unmarshal([]byte(fmt.Sprint(payload)), &item); err != nil {
			return nil, service.ErrAccountPoolSnapshotUnavailable
		}
		result[ids[i]] = item.Capacity
	}
	return result, nil
}

func (c *accountPoolSnapshotCache) WriteAccountPoolGeneration(ctx context.Context, generation, enabledEpoch string, items []service.PublicAccountPoolAccount, ttl time.Duration) error {
	ids := make([]int64, 0, len(items))
	statusIDs := make(map[string][]int64)
	for start := 0; start < len(items); start += 500 {
		end := start + 500
		if end > len(items) {
			end = len(items)
		}
		_, err := c.rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
			for _, item := range items[start:end] {
				payload, marshalErr := json.Marshal(item)
				if marshalErr != nil {
					return marshalErr
				}
				ids = append(ids, item.ID)
				statusIDs[item.Status.Code] = append(statusIDs[item.Status.Code], item.ID)
				pipe.Set(ctx, accountPoolItemKey(generation, item.ID), payload, ttl)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	meta := accountPoolGenerationMeta{
		SchemaVersion: service.AccountPoolSchemaVersion, Generation: generation, EnabledEpoch: enabledEpoch,
		AccountIDs: ids, StatusAccountIDs: statusIDs,
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	const publishScript = `
if redis.call("GET", KEYS[1]) ~= ARGV[1] then return 0 end
redis.call("SET", KEYS[2], ARGV[2], "PX", ARGV[3])
redis.call("SET", KEYS[3], "1", "PX", ARGV[3])
redis.call("SET", KEYS[4], ARGV[1], "PX", ARGV[3])
return 1`
	published, err := c.rdb.Eval(ctx, publishScript, []string{
		accountPoolLockKey,
		accountPoolMetaKey(generation),
		accountPoolCompleteKey(generation),
		accountPoolCurrentKey,
	}, generation, metaJSON, ttl.Milliseconds()).Int64()
	if err != nil {
		return err
	}
	if published != 1 {
		return service.ErrAccountPoolBuildLockLost
	}
	return nil
}

func (c *accountPoolSnapshotCache) ReadAccountPoolPage(ctx context.Context, enabledEpoch string, page, pageSize int, query service.AccountPoolListQuery) ([]service.PublicAccountPoolAccount, int64, error) {
	generation, err := c.rdb.Get(ctx, accountPoolCurrentKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, 0, service.ErrAccountPoolSnapshotNotReady
		}
		return nil, 0, service.ErrAccountPoolSnapshotUnavailable
	}
	values, err := c.rdb.MGet(ctx, accountPoolMetaKey(generation), accountPoolCompleteKey(generation)).Result()
	if err != nil {
		return nil, 0, service.ErrAccountPoolSnapshotUnavailable
	}
	if len(values) != 2 || values[0] == nil || values[1] != "1" {
		return nil, 0, service.ErrAccountPoolSnapshotNotReady
	}
	var meta accountPoolGenerationMeta
	if err := json.Unmarshal([]byte(fmt.Sprint(values[0])), &meta); err != nil || meta.SchemaVersion != service.AccountPoolSchemaVersion || meta.Generation != generation || meta.EnabledEpoch != enabledEpoch {
		return nil, 0, service.ErrAccountPoolSnapshotNotReady
	}
	ids := accountPoolMetaIDs(meta, query)
	total := int64(len(ids))
	if query.AccountID != nil {
		idx := sort.Search(len(meta.AccountIDs), func(i int) bool { return meta.AccountIDs[i] >= *query.AccountID })
		if idx >= len(meta.AccountIDs) || meta.AccountIDs[idx] != *query.AccountID ||
			(query.Status != "" && !accountPoolIDInSortedList(meta.StatusAccountIDs[query.Status], *query.AccountID)) ||
			(query.Relation != "" && !accountPoolIDInList(query.RelationAccountIDs, *query.AccountID)) {
			return []service.PublicAccountPoolAccount{}, 0, nil
		}
		ids = []int64{*query.AccountID}
		total = 1
	} else {
		start := (page - 1) * pageSize
		if start >= len(ids) {
			return []service.PublicAccountPoolAccount{}, total, nil
		}
		end := start + pageSize
		if end > len(ids) {
			end = len(ids)
		}
		ids = ids[start:end]
	}
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = accountPoolItemKey(generation, id)
	}
	if len(keys) == 0 {
		return []service.PublicAccountPoolAccount{}, total, nil
	}
	payloads, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, 0, service.ErrAccountPoolSnapshotUnavailable
	}
	items := make([]service.PublicAccountPoolAccount, 0, len(payloads))
	for _, payload := range payloads {
		if payload == nil {
			return nil, 0, service.ErrAccountPoolSnapshotUnavailable
		}
		var item service.PublicAccountPoolAccount
		if err := json.Unmarshal([]byte(fmt.Sprint(payload)), &item); err != nil {
			return nil, 0, service.ErrAccountPoolSnapshotUnavailable
		}
		items = append(items, item)
	}
	if query.AccountID != nil {
		total = int64(len(items))
	}
	return items, total, nil
}

// accountPoolMetaIDs 只在 generation 元数据上完成排序和筛选，再批量读取当前页账号。
func accountPoolMetaIDs(meta accountPoolGenerationMeta, query service.AccountPoolListQuery) []int64 {
	if query.AccountID != nil {
		return nil
	}
	if query.SortBy == service.AccountPoolSortByStatus && query.Status == "" {
		statuses := make([]string, 0, len(meta.StatusAccountIDs))
		for status := range meta.StatusAccountIDs {
			statuses = append(statuses, status)
		}
		sort.Strings(statuses)
		if query.SortOrder == service.AccountPoolSortDesc {
			slices.Reverse(statuses)
		}
		ids := make([]int64, 0, len(meta.AccountIDs))
		for _, status := range statuses {
			bucket := append([]int64(nil), meta.StatusAccountIDs[status]...)
			if query.SortOrder == service.AccountPoolSortDesc {
				slices.Reverse(bucket)
			}
			ids = append(ids, bucket...)
		}
		return filterAccountPoolIDsByRelation(ids, query)
	}
	ids := append([]int64(nil), meta.AccountIDs...)
	if query.Status != "" {
		ids = append([]int64(nil), meta.StatusAccountIDs[query.Status]...)
	}
	if query.SortOrder == service.AccountPoolSortDesc {
		slices.Reverse(ids)
	}
	return filterAccountPoolIDsByRelation(ids, query)
}

func accountPoolIDInSortedList(ids []int64, accountID int64) bool {
	idx := sort.Search(len(ids), func(i int) bool { return ids[i] >= accountID })
	return idx < len(ids) && ids[idx] == accountID
}

func accountPoolMetaKey(generation string) string {
	return accountPoolKeyPrefix + "generation:" + generation + ":meta"
}
func accountPoolCompleteKey(generation string) string {
	return accountPoolKeyPrefix + "generation:" + generation + ":complete"
}
func accountPoolItemKey(generation string, id int64) string {
	return accountPoolKeyPrefix + "generation:" + generation + ":account:" + strconv.FormatInt(id, 10)
}

func accountPoolPersonalUsageKey(key string) string {
	return accountPoolPersonalUsageKeyPrefix + key
}
