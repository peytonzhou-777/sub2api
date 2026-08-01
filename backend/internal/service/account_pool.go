package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const AccountPoolSchemaVersion = 2

var (
	ErrAccountPoolSnapshotUnavailable      = errors.New("account pool snapshot unavailable")
	ErrAccountPoolSnapshotNotReady         = errors.New("account pool snapshot not ready")
	ErrAccountPoolBuildLockNotAcquired     = errors.New("account pool build lock not acquired")
	ErrAccountPoolBuildLockLost            = errors.New("account pool build lock lost")
	ErrAccountPoolPersonalUsageNotFound    = errors.New("account pool personal usage account not found")
	ErrAccountPoolPersonalUsageUnsupported = errors.New("account pool personal usage unsupported")
	ErrAccountPoolPersonalUsageUnavailable = errors.New("account pool personal usage unavailable")
)

type AccountPoolFreshness string

const (
	AccountPoolFreshnessFresh       AccountPoolFreshness = "fresh"
	AccountPoolFreshnessStale       AccountPoolFreshness = "stale"
	AccountPoolFreshnessUnavailable AccountPoolFreshness = "unavailable"
)

// PublicAccountPoolCapacity 是号池允许公开的并发容量字段。
type PublicAccountPoolCapacity struct {
	CurrentConcurrency *int                 `json:"current_concurrency"`
	MaxConcurrency     int                  `json:"max_concurrency"`
	ObservedAt         *time.Time           `json:"observed_at"`
	State              AccountPoolFreshness `json:"state"`
}

// PublicAccountPoolUsageWindow 是平台用量窗口的白名单投影。
type PublicAccountPoolUsageWindow struct {
	Code        string               `json:"code"`
	Label       string               `json:"label"`
	UsedPercent *float64             `json:"used_percent"`
	ResetsAt    *time.Time           `json:"resets_at"`
	ObservedAt  *time.Time           `json:"observed_at"`
	State       AccountPoolFreshness `json:"state"`
}

type PublicAccountPoolModelStatus struct {
	Kind     string     `json:"kind"`
	Model    string     `json:"model"`
	ResumeAt *time.Time `json:"resume_at"`
}

// PublicAccountPoolStatus 只包含稳定状态码和脱敏恢复信息。
type PublicAccountPoolStatus struct {
	Code     string                         `json:"code"`
	Category string                         `json:"category,omitempty"`
	ResumeAt *time.Time                     `json:"resume_at"`
	Models   []PublicAccountPoolModelStatus `json:"models"`
}

// PublicAccountPoolAccount 是 HTTP 与 Redis 快照共用的显式白名单 DTO。
type PublicAccountPoolAccount struct {
	ID       int64  `json:"id"`
	Platform string `json:"platform"`
	Type     string `json:"type"`
	// 以下字段仅用于复刻管理员账号页的平台/类型展示，不包含任何凭据内容。
	AuthMode        string                         `json:"auth_mode,omitempty"`
	PlanType        string                         `json:"plan_type,omitempty"`
	PrivacyMode     string                         `json:"privacy_mode,omitempty"`
	AntigravityTier string                         `json:"antigravity_tier,omitempty"`
	Capacity        PublicAccountPoolCapacity      `json:"capacity"`
	UsageWindows    []PublicAccountPoolUsageWindow `json:"usage_windows"`
	ResetCount      *int                           `json:"reset_count"`
	ResetCountState string                         `json:"reset_count_state"`
	Status          PublicAccountPoolStatus        `json:"status"`
}

type AccountPoolPage struct {
	Items    []PublicAccountPoolAccount `json:"items"`
	Total    int64                      `json:"total"`
	Page     int                        `json:"page"`
	PageSize int                        `json:"page_size"`
	Pages    int                        `json:"pages"`
}

// AccountPoolPersonalUsageWindow 是当前用户在账号窗口内的本地用量汇总。
type AccountPoolPersonalUsageWindow struct {
	Code       string    `json:"code"`
	Label      string    `json:"label"`
	StartAt    time.Time `json:"start_at"`
	EndAt      time.Time `json:"end_at"`
	Requests   int64     `json:"requests"`
	Tokens     int64     `json:"tokens"`
	ActualCost float64   `json:"actual_cost"`
}

// AccountPoolPersonalUsage 是号池个人用量接口的私有响应 DTO。
type AccountPoolPersonalUsage struct {
	AccountID  int64                            `json:"account_id"`
	ObservedAt time.Time                        `json:"observed_at"`
	Windows    []AccountPoolPersonalUsageWindow `json:"windows"`
}

// AccountPoolPersonalUsageStats 是仓储一次查询返回的两个窗口聚合结果。
type AccountPoolPersonalUsageStats struct {
	FiveHour AccountPoolUsageMetrics
	SevenDay AccountPoolUsageMetrics
}

type AccountPoolUsageMetrics struct {
	Requests   int64
	Tokens     int64
	ActualCost float64
}

// AccountPoolSourceRecord 仅在构建进程内存在，Extra 经过映射后绝不进入公开快照。
type AccountPoolSourceRecord struct {
	ID                     int64
	Platform               string
	Type                   string
	Concurrency            int
	Status                 string
	Schedulable            bool
	RateLimitResetAt       *time.Time
	OverloadUntil          *time.Time
	TempUnschedulableUntil *time.Time
	SessionWindowEnd       *time.Time
	ParentAccountID        *int64
	QuotaDimension         string
	Credentials            map[string]any
	ParentCredentials      map[string]any
	ParentExtra            map[string]any
	Extra                  map[string]any
	ResetCount             *int
	ResetCountObservedAt   *time.Time
}

type AccountPoolSource interface {
	ListAccountPoolBuildBatch(ctx context.Context, afterID int64, limit int) ([]AccountPoolSourceRecord, bool, error)
	ListAccountPoolPage(ctx context.Context, page, pageSize int, accountID *int64) ([]AccountPoolSourceRecord, int64, error)
}

// AccountPoolConcurrencyReader 为号池提供只读的批量并发观测。
type AccountPoolConcurrencyReader interface {
	GetAccountConcurrencyBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error)
}

type AccountPoolSnapshotCache interface {
	AcquireAccountPoolBuildLock(ctx context.Context, owner string, ttl time.Duration) (bool, error)
	RenewAccountPoolBuildLock(ctx context.Context, owner string, ttl time.Duration) (bool, error)
	ReleaseAccountPoolBuildLock(ctx context.Context, owner string) error
	ReadAccountPoolPreviousCapacities(ctx context.Context, enabledEpoch string, accountIDs []int64) (map[int64]PublicAccountPoolCapacity, error)
	WriteAccountPoolGeneration(ctx context.Context, generation, enabledEpoch string, items []PublicAccountPoolAccount, ttl time.Duration) error
	ReadAccountPoolPage(ctx context.Context, enabledEpoch string, page, pageSize int, accountID *int64) ([]PublicAccountPoolAccount, int64, error)
}

// AccountPoolPersonalUsageCache 是 Redis 私有短缓存的可选能力。
// 快照测试替身未实现时，服务会自动退回进程内缓存。
type AccountPoolPersonalUsageCache interface {
	GetAccountPoolPersonalUsage(ctx context.Context, key string) (*AccountPoolPersonalUsage, bool, error)
	SetAccountPoolPersonalUsage(ctx context.Context, key string, value *AccountPoolPersonalUsage, ttl time.Duration) error
}

// AccountPoolPersonalUsageReader 只读取本地 usage_logs，不触发任何账号操作。
type AccountPoolPersonalUsageReader interface {
	GetUserAccountPersonalUsage(ctx context.Context, userID, accountID int64, fiveHourStart, sevenDayStart, end time.Time) (*AccountPoolPersonalUsageStats, error)
}

type AccountPoolOptions struct {
	BuildBatchSize int
	SnapshotTTL    time.Duration
	UsageFresh     time.Duration
	UsageRetention time.Duration
	BuildLockTTL   time.Duration
}

type AccountPoolService struct {
	source              AccountPoolSource
	cache               AccountPoolSnapshotCache
	concurrency         AccountPoolConcurrencyReader
	options             AccountPoolOptions
	now                 func() time.Time
	personalUsageReader AccountPoolPersonalUsageReader
	personalUsageMu     sync.Mutex
	personalUsageCache  map[string]accountPoolPersonalUsageCacheEntry
	personalUsageSF     singleflight.Group
}

type accountPoolPersonalUsageCacheEntry struct {
	value     *AccountPoolPersonalUsage
	expiresAt time.Time
}

func NewAccountPoolService(source AccountPoolSource, cache AccountPoolSnapshotCache, concurrency AccountPoolConcurrencyReader, options AccountPoolOptions) *AccountPoolService {
	if options.BuildBatchSize <= 0 {
		options.BuildBatchSize = 500
	}
	if options.SnapshotTTL <= 0 {
		options.SnapshotTTL = 10 * time.Minute
	}
	if options.UsageFresh <= 0 {
		options.UsageFresh = 15 * time.Minute
	}
	if options.UsageRetention < options.UsageFresh {
		options.UsageRetention = time.Hour
	}
	if options.BuildLockTTL <= 0 {
		options.BuildLockTTL = 2 * time.Minute
	}
	return &AccountPoolService{
		source: source, cache: cache, concurrency: concurrency, options: options, now: time.Now,
		personalUsageCache: make(map[string]accountPoolPersonalUsageCacheEntry),
	}
}

// SetPersonalUsageReader 注入本地用量读取器，保持号池快照构建与个人统计解耦。
func (s *AccountPoolService) SetPersonalUsageReader(reader AccountPoolPersonalUsageReader) {
	if s == nil {
		return
	}
	s.personalUsageReader = reader
}

// Reconcile 构建完整新代次；只有缓存全部写入成功后才由缓存实现切换当前指针。
func (s *AccountPoolService) Reconcile(ctx context.Context, generation, enabledEpoch string) error {
	if s == nil || s.source == nil || s.cache == nil {
		return ErrAccountPoolSnapshotUnavailable
	}
	locked, err := s.cache.AcquireAccountPoolBuildLock(ctx, generation, s.options.BuildLockTTL)
	if err != nil {
		return err
	}
	if !locked {
		return ErrAccountPoolBuildLockNotAcquired
	}
	defer func() { _ = s.cache.ReleaseAccountPoolBuildLock(context.Background(), generation) }()

	buildCtx, cancelBuild := context.WithCancel(ctx)
	stopRenewal, renewalDone := make(chan struct{}), make(chan struct{})
	renewalErr := make(chan error, 1)
	go s.renewBuildLock(buildCtx, cancelBuild, generation, stopRenewal, renewalDone, renewalErr)
	defer func() {
		close(stopRenewal)
		cancelBuild()
		<-renewalDone
	}()

	items := make([]PublicAccountPoolAccount, 0, s.options.BuildBatchSize)
	afterID := int64(0)
	for {
		records, hasMore, listErr := s.source.ListAccountPoolBuildBatch(buildCtx, afterID, s.options.BuildBatchSize)
		if listErr != nil {
			if leaseErr := readAccountPoolRenewalError(renewalErr); leaseErr != nil {
				return leaseErr
			}
			return fmt.Errorf("list account pool build batch: %w", listErr)
		}
		if len(records) == 0 {
			break
		}
		counts, countErr := s.readConcurrency(buildCtx, records)
		previousCapacities := map[int64]PublicAccountPoolCapacity(nil)
		if countErr != nil {
			ids := accountPoolRecordIDs(records)
			previousCapacities, err = s.cache.ReadAccountPoolPreviousCapacities(buildCtx, enabledEpoch, ids)
			if err != nil && !errors.Is(err, ErrAccountPoolSnapshotNotReady) {
				return fmt.Errorf("preserve previous account pool concurrency: %w", err)
			}
		}
		now := s.now().UTC()
		for _, record := range records {
			item := s.mapPublicAccount(record, counts, now)
			if previous, ok := previousCapacities[record.ID]; ok {
				previous.MaxConcurrency = record.Concurrency
				item.Capacity = previous
			}
			items = append(items, item)
			afterID = record.ID
		}
		if !hasMore {
			break
		}
	}
	if enabledEpoch == "" {
		return ErrAccountPoolSnapshotNotReady
	}
	if leaseErr := readAccountPoolRenewalError(renewalErr); leaseErr != nil {
		return leaseErr
	}
	renewed, err := s.cache.RenewAccountPoolBuildLock(buildCtx, generation, s.options.BuildLockTTL)
	if err != nil {
		return fmt.Errorf("renew account pool build lock before publish: %w", err)
	}
	if !renewed {
		return ErrAccountPoolBuildLockLost
	}
	if err := s.cache.WriteAccountPoolGeneration(buildCtx, generation, enabledEpoch, items, s.options.SnapshotTTL); err != nil {
		return fmt.Errorf("write account pool generation: %w", err)
	}
	return nil
}

// List 优先读取完整 Redis 代次，失败时只从数据库重建基础降级视图。
func (s *AccountPoolService) List(ctx context.Context, enabledEpoch string, page, pageSize int, accountID *int64) (*AccountPoolPage, error) {
	if enabledEpoch == "" {
		return nil, ErrAccountPoolSnapshotNotReady
	}
	if s.cache != nil {
		items, total, err := s.cache.ReadAccountPoolPage(ctx, enabledEpoch, page, pageSize, accountID)
		if err == nil {
			return newAccountPoolPage(items, total, page, pageSize), nil
		}
		if errors.Is(err, ErrAccountPoolSnapshotNotReady) {
			return nil, err
		}
	}
	if s.source == nil {
		return nil, ErrAccountPoolSnapshotUnavailable
	}
	records, total, err := s.source.ListAccountPoolPage(ctx, page, pageSize, accountID)
	if err != nil {
		return nil, fmt.Errorf("account pool database fallback: %w", err)
	}
	items := make([]PublicAccountPoolAccount, 0, len(records))
	now := s.now().UTC()
	for _, record := range records {
		item := s.mapPublicAccount(record, nil, now)
		// 数据库降级只展示基础字段，不能把持久化观测冒充当前动态状态。
		item.UsageWindows = []PublicAccountPoolUsageWindow{}
		item.ResetCount = nil
		if item.ResetCountState != "shared" && item.ResetCountState != "not_applicable" {
			item.ResetCountState = "unavailable"
		}
		items = append(items, item)
	}
	return newAccountPoolPage(items, total, page, pageSize), nil
}

const accountPoolPersonalUsageCacheTTL = 30 * time.Second

// GetPersonalUsage 获取当前用户在号池指定账号的 5h/7d 本地用量。
// 先验证账号属于当前启用代次，再读取私有短缓存，避免软删除账号命中旧数据。
func (s *AccountPoolService) GetPersonalUsage(ctx context.Context, enabledEpoch string, userID, accountID int64) (*AccountPoolPersonalUsage, error) {
	if s == nil || enabledEpoch == "" || userID <= 0 || accountID <= 0 {
		return nil, ErrAccountPoolPersonalUsageNotFound
	}
	page, err := s.List(ctx, enabledEpoch, 1, 1, &accountID)
	if err != nil {
		return nil, err
	}
	if page == nil || len(page.Items) != 1 || page.Items[0].ID != accountID {
		return nil, ErrAccountPoolPersonalUsageNotFound
	}
	account := page.Items[0]
	if !supportsAccountPoolPersonalUsage(account.Platform, account.Type) {
		return nil, ErrAccountPoolPersonalUsageUnsupported
	}

	now := s.now().UTC()
	fiveHourStart := accountPoolWindowStart(account.UsageWindows, "5h", 5*time.Hour, now)
	sevenDayStart := accountPoolWindowStart(account.UsageWindows, "7d", 7*24*time.Hour, now)
	cacheKey := fmt.Sprintf(
		"%s:%d:%d:%d:%d",
		enabledEpoch, userID, accountID, fiveHourStart.Unix(), sevenDayStart.Unix(),
	)
	if value, ok := s.getPersonalUsageCache(ctx, cacheKey, now); ok {
		return value, nil
	}

	result, err, _ := s.personalUsageSF.Do(cacheKey, func() (any, error) {
		if value, ok := s.getPersonalUsageCache(ctx, cacheKey, now); ok {
			return value, nil
		}
		if s.personalUsageReader == nil {
			return nil, ErrAccountPoolPersonalUsageUnavailable
		}
		stats, err := s.personalUsageReader.GetUserAccountPersonalUsage(
			ctx, userID, accountID, fiveHourStart, sevenDayStart, now,
		)
		if err != nil {
			return nil, fmt.Errorf("get account pool personal usage: %w", err)
		}
		if stats == nil {
			return nil, ErrAccountPoolPersonalUsageUnavailable
		}
		value := &AccountPoolPersonalUsage{
			AccountID:  accountID,
			ObservedAt: now,
			Windows: []AccountPoolPersonalUsageWindow{
				{Code: "5h", Label: "5h", StartAt: fiveHourStart, EndAt: now,
					Requests: stats.FiveHour.Requests, Tokens: stats.FiveHour.Tokens, ActualCost: stats.FiveHour.ActualCost},
				{Code: "7d", Label: "7d", StartAt: sevenDayStart, EndAt: now,
					Requests: stats.SevenDay.Requests, Tokens: stats.SevenDay.Tokens, ActualCost: stats.SevenDay.ActualCost},
			},
		}
		s.setPersonalUsageCache(ctx, cacheKey, value, now.Add(accountPoolPersonalUsageCacheTTL))
		return value, nil
	})
	if err != nil {
		return nil, err
	}
	value, ok := result.(*AccountPoolPersonalUsage)
	if !ok || value == nil {
		return nil, ErrAccountPoolPersonalUsageUnavailable
	}
	return value, nil
}

func supportsAccountPoolPersonalUsage(platform, accountType string) bool {
	if accountType != AccountTypeOAuth && accountType != AccountTypeSetupToken {
		return false
	}
	return platform == PlatformOpenAI || platform == PlatformAnthropic
}

func accountPoolWindowStart(windows []PublicAccountPoolUsageWindow, code string, duration time.Duration, now time.Time) time.Time {
	for _, window := range windows {
		if window.Code != code || window.ResetsAt == nil || !now.Before(window.ResetsAt.UTC()) {
			continue
		}
		return window.ResetsAt.UTC().Add(-duration)
	}
	return now.Add(-duration)
}

func (s *AccountPoolService) getPersonalUsageCache(ctx context.Context, key string, now time.Time) (*AccountPoolPersonalUsage, bool) {
	if cache, ok := s.cache.(AccountPoolPersonalUsageCache); ok {
		if value, hit, err := cache.GetAccountPoolPersonalUsage(ctx, key); err == nil && hit && value != nil {
			return value, true
		}
	}
	s.personalUsageMu.Lock()
	defer s.personalUsageMu.Unlock()
	entry, ok := s.personalUsageCache[key]
	if !ok || entry.value == nil || !now.Before(entry.expiresAt) {
		if ok {
			delete(s.personalUsageCache, key)
		}
		return nil, false
	}
	return entry.value, true
}

func (s *AccountPoolService) setPersonalUsageCache(ctx context.Context, key string, value *AccountPoolPersonalUsage, expiresAt time.Time) {
	s.personalUsageMu.Lock()
	s.personalUsageCache[key] = accountPoolPersonalUsageCacheEntry{value: value, expiresAt: expiresAt}
	s.personalUsageMu.Unlock()
	if cache, ok := s.cache.(AccountPoolPersonalUsageCache); ok {
		_ = cache.SetAccountPoolPersonalUsage(ctx, key, value, time.Until(expiresAt))
	}
}

func newAccountPoolPage(items []PublicAccountPoolAccount, total int64, page, pageSize int) *AccountPoolPage {
	pages := int(math.Ceil(float64(total) / float64(pageSize)))
	if pages < 1 {
		pages = 1
	}
	return &AccountPoolPage{Items: items, Total: total, Page: page, PageSize: pageSize, Pages: pages}
}

func (s *AccountPoolService) readConcurrency(ctx context.Context, records []AccountPoolSourceRecord) (map[int64]int, error) {
	if s.concurrency == nil {
		return nil, errors.New("account pool concurrency service unavailable")
	}
	ids := make([]int64, len(records))
	for i := range records {
		ids[i] = records[i].ID
	}
	counts, err := s.concurrency.GetAccountConcurrencyBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	return counts, nil
}

func accountPoolRecordIDs(records []AccountPoolSourceRecord) []int64 {
	ids := make([]int64, len(records))
	for i := range records {
		ids[i] = records[i].ID
	}
	return ids
}

// renewBuildLock 定期续租构建锁；失锁时取消构建，阻止不再持锁的代次发布。
func (s *AccountPoolService) renewBuildLock(ctx context.Context, cancel context.CancelFunc, owner string, stop <-chan struct{}, done chan<- struct{}, result chan<- error) {
	defer close(done)
	interval := s.options.BuildLockTTL / 3
	if interval <= 0 {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			renewed, err := s.cache.RenewAccountPoolBuildLock(ctx, owner, s.options.BuildLockTTL)
			if err != nil {
				select {
				case result <- fmt.Errorf("renew account pool build lock: %w", err):
				default:
				}
				cancel()
				return
			}
			if !renewed {
				select {
				case result <- ErrAccountPoolBuildLockLost:
				default:
				}
				cancel()
				return
			}
		case <-stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

func readAccountPoolRenewalError(result <-chan error) error {
	select {
	case err := <-result:
		return err
	default:
		return nil
	}
}

func (s *AccountPoolService) mapPublicAccount(record AccountPoolSourceRecord, counts map[int64]int, now time.Time) PublicAccountPoolAccount {
	capacity := PublicAccountPoolCapacity{MaxConcurrency: record.Concurrency, State: AccountPoolFreshnessUnavailable}
	if current, ok := counts[record.ID]; ok {
		capacity.CurrentConcurrency = &current
		capacity.ObservedAt = timePtr(now)
		capacity.State = AccountPoolFreshnessFresh
	}
	resetState := "not_applicable"
	var resetCount *int
	if record.ParentAccountID != nil || record.QuotaDimension == "spark" {
		resetState = "shared"
	} else if record.Platform == PlatformOpenAI && record.Type == AccountTypeOAuth {
		resetState = string(s.observationState(record.ResetCountObservedAt, now))
		if resetState == string(AccountPoolFreshnessFresh) && record.ResetCount != nil && *record.ResetCount >= 0 {
			value := *record.ResetCount
			resetCount = &value
		} else if resetState == string(AccountPoolFreshnessFresh) {
			resetState = string(AccountPoolFreshnessUnavailable)
		}
	}
	return PublicAccountPoolAccount{
		ID: record.ID, Platform: record.Platform, Type: record.Type,
		AuthMode: publicAccountPoolAuthMode(record), PlanType: publicAccountPoolPlanType(record),
		PrivacyMode:     publicAccountPoolPrivacyMode(record),
		AntigravityTier: publicAccountPoolAntigravityTier(record),
		Capacity:        capacity,
		UsageWindows:    s.mapUsageWindows(record, now), ResetCount: resetCount, ResetCountState: resetState,
		Status: mapAccountPoolStatus(record, now),
	}
}

// publicAccountPoolAuthMode 提取管理员平台徽章所需的非敏感认证模式。
func publicAccountPoolAuthMode(record AccountPoolSourceRecord) string {
	if record.Platform != PlatformOpenAI || record.Type != AccountTypeOAuth {
		return ""
	}
	value, _ := accountPoolCredentialValue(record, "auth_mode").(string)
	return strings.TrimSpace(value)
}

// publicAccountPoolPlanType 使用管理员页相同的优先级，仅返回订阅档位文本。
func publicAccountPoolPlanType(record AccountPoolSourceRecord) string {
	extra := record.Extra
	if record.Platform == PlatformGrok {
		if billing, ok := extra["grok_billing_snapshot"].(map[string]any); ok {
			if value, _ := billing["plan"].(string); strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
		if quota, ok := extra["grok_quota_snapshot"].(map[string]any); ok {
			if value, _ := quota["subscription_tier"].(string); strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	if value, _ := accountPoolCredentialValue(record, "plan_type").(string); strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	if value, _ := extra["subscription_tier"].(string); strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return ""
}

func publicAccountPoolPrivacyMode(record AccountPoolSourceRecord) string {
	value, _ := accountPoolExtraValue(record, "privacy_mode").(string)
	return strings.TrimSpace(value)
}

func publicAccountPoolAntigravityTier(record AccountPoolSourceRecord) string {
	if record.Platform != PlatformAntigravity {
		return ""
	}
	loadCodeAssist, ok := accountPoolExtraValue(record, "load_code_assist").(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"paidTier", "currentTier"} {
		if tier, ok := loadCodeAssist[key].(map[string]any); ok {
			if value, _ := tier["id"].(string); strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func accountPoolCredentialValue(record AccountPoolSourceRecord, key string) any {
	if value, ok := record.Credentials[key]; ok && value != nil && strings.TrimSpace(fmt.Sprint(value)) != "" {
		return value
	}
	return record.ParentCredentials[key]
}

func accountPoolExtraValue(record AccountPoolSourceRecord, key string) any {
	if value, ok := record.Extra[key]; ok && value != nil && strings.TrimSpace(fmt.Sprint(value)) != "" {
		return value
	}
	return record.ParentExtra[key]
}

func (s *AccountPoolService) mapUsageWindows(record AccountPoolSourceRecord, now time.Time) []PublicAccountPoolUsageWindow {
	if record.Platform == PlatformOpenAI && (record.Type == AccountTypeOAuth || record.Type == AccountTypeSetupToken) {
		observed := parseAccountPoolTime(record.Extra["codex_usage_updated_at"])
		return []PublicAccountPoolUsageWindow{
			s.mapWindow("5h", "5h", record.Extra["codex_5h_used_percent"], record.Extra["codex_5h_reset_at"], observed, now, false),
			s.mapWindow("7d", "7d", record.Extra["codex_7d_used_percent"], record.Extra["codex_7d_reset_at"], observed, now, false),
		}
	}
	if record.Platform == PlatformAnthropic && (record.Type == AccountTypeOAuth || record.Type == AccountTypeSetupToken) {
		observed := parseAccountPoolTime(record.Extra["passive_usage_sampled_at"])
		return []PublicAccountPoolUsageWindow{
			s.mapWindow("5h", "5h", record.Extra["session_window_utilization"], record.SessionWindowEnd, observed, now, true),
			s.mapWindow("7d", "7d", record.Extra["passive_usage_7d_utilization"], record.Extra["passive_usage_7d_reset"], observed, now, true),
			s.mapWindow("7d_oi", "7d F", record.Extra["passive_usage_7d_oi_utilization"], record.Extra["passive_usage_7d_oi_reset"], observed, now, true),
		}
	}
	return []PublicAccountPoolUsageWindow{}
}

func (s *AccountPoolService) mapWindow(code, label string, usedRaw, resetRaw any, observed *time.Time, now time.Time, ratio bool) PublicAccountPoolUsageWindow {
	state := s.observationState(observed, now)
	window := PublicAccountPoolUsageWindow{Code: code, Label: label, ObservedAt: observed, State: state}
	if state == AccountPoolFreshnessUnavailable || usedRaw == nil {
		window.State = AccountPoolFreshnessUnavailable
		return window
	}
	used := parseExtraFloat64(usedRaw)
	if ratio {
		used *= 100
	}
	used = math.Max(0, math.Min(100, used))
	window.UsedPercent = &used
	window.ResetsAt = parseAccountPoolTime(resetRaw)
	return window
}

func (s *AccountPoolService) observationState(observed *time.Time, now time.Time) AccountPoolFreshness {
	if observed == nil || observed.After(now.Add(time.Minute)) {
		return AccountPoolFreshnessUnavailable
	}
	age := now.Sub(*observed)
	if age <= s.options.UsageFresh {
		return AccountPoolFreshnessFresh
	}
	if age <= s.options.UsageRetention {
		return AccountPoolFreshnessStale
	}
	return AccountPoolFreshnessUnavailable
}

func mapAccountPoolStatus(record AccountPoolSourceRecord, now time.Time) PublicAccountPoolStatus {
	status := PublicAccountPoolStatus{Code: "active", Models: []PublicAccountPoolModelStatus{}}
	switch {
	case record.Status == StatusDisabled:
		status.Code = "disabled"
	case record.Status == StatusError:
		status.Code, status.Category = "error", "needs_admin"
	case record.TempUnschedulableUntil != nil && now.Before(*record.TempUnschedulableUntil):
		status.Code, status.ResumeAt = "temporarily_unavailable", record.TempUnschedulableUntil
	case record.OverloadUntil != nil && now.Before(*record.OverloadUntil):
		status.Code, status.ResumeAt = "overloaded", record.OverloadUntil
	case record.RateLimitResetAt != nil && now.Before(*record.RateLimitResetAt):
		status.Code, status.ResumeAt = "rate_limited", record.RateLimitResetAt
	case !record.Schedulable:
		status.Code = "paused"
	case accountPoolQuotaExceeded(record.Extra):
		status.Code = "quota_exceeded"
	}
	status.Models = mapAccountPoolModelStatuses(record.Extra, now)
	return status
}

func mapAccountPoolModelStatuses(extra map[string]any, now time.Time) []PublicAccountPoolModelStatus {
	raw, ok := extra["model_rate_limits"].(map[string]any)
	if !ok {
		return []PublicAccountPoolModelStatus{}
	}
	items := make([]PublicAccountPoolModelStatus, 0, len(raw))
	for model, rawInfo := range raw {
		info, ok := rawInfo.(map[string]any)
		if !ok {
			continue
		}
		resumeAt := parseAccountPoolTime(info["rate_limit_reset_at"])
		if resumeAt == nil || !now.Before(*resumeAt) {
			continue
		}
		kind := "rate_limit"
		if model == "AICredits" {
			kind = "credits_exhausted"
		}
		items = append(items, PublicAccountPoolModelStatus{Kind: kind, Model: publicAccountPoolModelName(model), ResumeAt: resumeAt})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Model < items[j].Model })
	return items
}

func publicAccountPoolModelName(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case model == "AICredits":
		return "AI Credits"
	case strings.Contains(normalized, "opus"):
		return "Opus"
	case strings.Contains(normalized, "sonnet"):
		return "Sonnet"
	case strings.Contains(normalized, "gemini") && strings.Contains(normalized, "flash"):
		return "Gemini Flash"
	case strings.Contains(normalized, "gemini") && strings.Contains(normalized, "pro"):
		return "Gemini Pro"
	default:
		return "Other"
	}
}

func accountPoolQuotaExceeded(extra map[string]any) bool {
	for _, pair := range [][2]string{{"quota_used", "quota_limit"}, {"quota_daily_used", "quota_daily_limit"}, {"quota_weekly_used", "quota_weekly_limit"}} {
		limit := parseExtraFloat64(extra[pair[1]])
		if limit > 0 && parseExtraFloat64(extra[pair[0]]) >= limit {
			return true
		}
	}
	return false
}

func parseAccountPoolTime(raw any) *time.Time {
	switch value := raw.(type) {
	case nil:
		return nil
	case *time.Time:
		return value
	case time.Time:
		return &value
	case int64:
		parsed := time.Unix(value, 0).UTC()
		return &parsed
	case float64:
		parsed := time.Unix(int64(value), 0).UTC()
		return &parsed
	default:
		parsed, err := parseTime(fmt.Sprint(value))
		if err != nil {
			return nil
		}
		parsed = parsed.UTC()
		return &parsed
	}
}

func timePtr(value time.Time) *time.Time { return &value }
