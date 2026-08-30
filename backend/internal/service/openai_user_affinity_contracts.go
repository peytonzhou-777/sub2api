package service

import (
	"context"
	"errors"
	"time"
)

// ErrOpenAIUserAffinityPlacementStale 表示归属投影在并发收敛期间已经失效。
// 调度层应将其视为 placement miss 并重新选择，而不是转换成 503。
var ErrOpenAIUserAffinityPlacementStale = errors.New("openai user affinity placement stale")

// OpenAIUserAffinityStore 是账号仓储可选的用户粘性状态能力。
// 通过可选接口接入，保持现有测试替身和非 OpenAI 调度调用方兼容。
type OpenAIUserAffinityStore interface {
	GetOpenAIUserPlacement(ctx context.Context, userID int64, scopeKey string) (*OpenAIUserPlacement, error)
	UpsertOpenAIUserPlacement(ctx context.Context, placement OpenAIUserPlacement) error
	RecordOpenAIUserPlacementEvent(ctx context.Context, event OpenAIUserPlacementEvent) error
}

// OpenAIUserAffinityMultiSlotStore 提供多槽位及会话绑定的权威数据库读路径。
// P1 只启用只读兼容层，后续阶段在同一契约上增加原子状态转换。
type OpenAIUserAffinityMultiSlotStore interface {
	ListOpenAIUserResidentSlots(ctx context.Context, userID int64, scopeKey string) ([]OpenAIUserResidentSlot, error)
	GetOpenAIUserConversationBinding(ctx context.Context, userID, apiKeyID int64, scopeKey, conversationHash string) (*OpenAIUserConversationBinding, error)
	GetOpenAIUserConversationBindingByAlias(ctx context.Context, userID, apiKeyID int64, scopeKey, aliasType, aliasHash string) (*OpenAIUserConversationBinding, error)
}

// OpenAIUserAffinityResidentSlotMaintenanceStore 让配置减槽和 TTL 缩短按当前 scope 幂等收敛。
type OpenAIUserAffinityResidentSlotMaintenanceStore interface {
	ConvergeOpenAIUserResidentSlots(ctx context.Context, userID int64, scopeKey string, config OpenAIUserAffinityConfig, now time.Time) error
	EvictOpenAIUserResidentSlot(ctx context.Context, userID int64, scopeKey string, slotID, accountID, generation int64, reason string, now time.Time) (bool, error)
}

// OpenAIUserAffinityResetExclusionStore 提供管理员整组重置后的一次性账号排除事实。
type OpenAIUserAffinityResetExclusionStore interface {
	ListOpenAIUserAffinityResetExcludedAccountIDs(ctx context.Context, userID int64, scopeKey string) ([]int64, error)
}

// OpenAIUserAffinityConversationStore 负责会话绑定的 provisional、首输出提交和失败回滚。
type OpenAIUserAffinityConversationStore interface {
	OpenAIUserAffinityMultiSlotStore
	ReserveOpenAIUserConversationBinding(ctx context.Context, reservation OpenAIUserConversationReservation) (*OpenAIUserConversationBinding, bool, error)
	CommitOpenAIUserConversationBinding(ctx context.Context, transition OpenAIUserConversationTransition) (bool, error)
	RollbackOpenAIUserConversationBinding(ctx context.Context, transition OpenAIUserConversationTransition) (bool, error)
}

// OpenAIUserAffinityActiveRoutingStore 提供新会话活动路由与账号软驻留快照。
type OpenAIUserAffinityActiveRoutingStore interface {
	GetOpenAIUserActiveRoute(ctx context.Context, userID int64, scopeKey string) (*OpenAIUserActiveRoute, error)
	ListOpenAIAccountSoftOccupancies(ctx context.Context, accountIDs []int64) (map[int64]OpenAIAccountSoftOccupancy, error)
}

// OpenAIUserAffinityConversationFailoverStore 以 pending 字段预留会话级切号，提交前不覆盖原绑定。
type OpenAIUserAffinityConversationFailoverStore interface {
	ReserveOpenAIUserConversationFailover(ctx context.Context, reservation OpenAIUserConversationFailoverReservation) (*OpenAIUserConversationTransition, bool, error)
	ReserveOpenAIUserResidentSlotReplacement(ctx context.Context, reservation OpenAIUserResidentSlotReplacementReservation) (*OpenAIUserConversationTransition, bool, error)
}

// OpenAIUserAffinityCandidateStore 提供新居民装箱所需的当前触达容量快照。
type OpenAIUserAffinityCandidateStore interface {
	GetOpenAIUserAffinityCandidateStats(ctx context.Context, userID int64, accountIDs []int64) (map[int64]OpenAIUserAffinityCandidate, error)
}

type OpenAIUserAffinityReconciler interface {
	ReconcileOpenAIUserAffinity(ctx context.Context, now time.Time) (map[string]int64, error)
}

// OpenAIUserAffinityRuntimeStore 提供新居民原子装箱和成功触达刷新能力。
type OpenAIUserAffinityRuntimeStore interface {
	AssignOpenAIUserAffinityPlacement(ctx context.Context, placement OpenAIUserPlacement, config OpenAIUserAffinityConfig) (bool, error)
	OpenAIUserAffinityTouchStore
	ConfirmOpenAIUserAffinitySuccess(ctx context.Context, incident OpenAIUserAffinityIncidentIdentity) error
	RollbackOpenAIUserAffinityPlacement(ctx context.Context, transition OpenAIUserAffinityProvisionalTransition, config OpenAIUserAffinityConfig) (bool, error)
	RecordOpenAIUserAffinityCapacityFailure(ctx context.Context, incident OpenAIUserAffinityIncidentIdentity, requestIDHash, reason string, config OpenAIUserAffinityConfig) (*time.Time, error)
	GetOpenAIUserAffinityMigrationAuthorizedAt(ctx context.Context, incident OpenAIUserAffinityIncidentIdentity) (*time.Time, error)
	MigrateOpenAIUserAffinityPlacement(ctx context.Context, userID, sourceAccountID, targetAccountID, generation int64, scopeKey, provisionalToken, reason string, config OpenAIUserAffinityConfig) (bool, error)
	BeginOpenAIUserAffinityReentry(ctx context.Context, input OpenAIUserAffinityReentryBegin) (*OpenAIUserAffinityReentryAdmission, error)
	ActivateOpenAIUserAffinityReentry(ctx context.Context, input OpenAIUserAffinityReentryTransition) (bool, error)
	FailOpenAIUserAffinityReentryLeader(ctx context.Context, input OpenAIUserAffinityReentryTransition) (bool, error)
	TakeoverOpenAIUserAffinityReentry(ctx context.Context, input OpenAIUserAffinityReentryTakeover) (*OpenAIUserAffinityReentryAdmission, error)
	CompleteOpenAIUserAffinityReentry(ctx context.Context, accountID, userID, generation int64, batchToken string) error
	PredictOpenAIUserAffinityDemand(ctx context.Context, userID int64, quantile float64) (OpenAIUserAffinityDemand, error)
}

// OpenAIUserAffinityTouchStore 只承载成功触达事实，便于协议成功钩子独立验证。
type OpenAIUserAffinityTouchStore interface {
	TouchOpenAIUserAffinity(ctx context.Context, userID, accountID, generation int64, scopeKey string, config OpenAIUserAffinityConfig) error
}

// OpenAIUserAffinitySuccessStore 将最终成功与 accepted 触达刷新分离。
type OpenAIUserAffinitySuccessStore interface {
	ConfirmOpenAIUserAffinitySuccess(ctx context.Context, incident OpenAIUserAffinityIncidentIdentity) error
}

// OpenAIUserAffinityProvisionalStore 负责失败请求的归属 CAS 回滚。
type OpenAIUserAffinityProvisionalStore interface {
	RollbackOpenAIUserAffinityPlacement(ctx context.Context, transition OpenAIUserAffinityProvisionalTransition, config OpenAIUserAffinityConfig) (bool, error)
}

// OpenAIUserAffinityReentryQueue 只保存跨实例协调元数据，不保存请求正文或业务响应。
type OpenAIUserAffinityReentryQueue interface {
	InitializeOpenAIUserAffinityReentry(ctx context.Context, admission OpenAIUserAffinityReentryAdmission) error
	EnqueueOpenAIUserAffinityFollower(ctx context.Context, admission OpenAIUserAffinityReentryAdmission) error
	PollOpenAIUserAffinityFollower(ctx context.Context, admission OpenAIUserAffinityReentryAdmission, now time.Time) (OpenAIUserAffinityFollowerPoll, error)
	ActivateOpenAIUserAffinityFollowers(ctx context.Context, admission OpenAIUserAffinityReentryAdmission, now time.Time) (bool, error)
	MarkOpenAIUserAffinityLeaderFailed(ctx context.Context, admission OpenAIUserAffinityReentryAdmission) error
	AcknowledgeOpenAIUserAffinityFollower(ctx context.Context, admission OpenAIUserAffinityReentryAdmission, now time.Time) (bool, error)
	RemoveOpenAIUserAffinityFollower(ctx context.Context, admission OpenAIUserAffinityReentryAdmission) error
}

type OpenAIUserAffinityReentryBegin struct {
	UserID, AccountID, Generation int64
	ScopeKey                      string
	BatchToken, LeaderToken       string
	LeaderLeaseUntil              time.Time
	Config                        OpenAIUserAffinityConfig
}

type OpenAIUserAffinityReentryTransition struct {
	UserID, AccountID, Generation int64
	CoordinationGeneration        int64
	ScopeKey                      string
	BatchToken, LeaderToken       string
	LeaderVersion                 int64
}

type OpenAIUserAffinityReentryTakeover struct {
	UserID, AccountID, Generation int64
	CoordinationGeneration        int64
	ScopeKey                      string
	BatchToken, WaiterToken       string
	ExpectedLeaderVersion         int64
	LeaderLeaseUntil              time.Time
}

type OpenAIUserAffinityReentryAdmission struct {
	Required   bool
	Leader     bool
	AccountID  int64
	UserID     int64
	Generation int64
	// CoordinationGeneration 标识账号+用户共享回流批次，不等同于各 scope 的归属 generation。
	CoordinationGeneration int64
	ScopeKey               string
	BatchToken             string
	LeaderToken            string
	WaiterToken            string
	LeaderVersion          int64
	LeaderLeaseUntil       time.Time
	ReentryState           string
	Deadline               time.Time
	JitterMinMS            int
	JitterMaxMS            int
	MaxFollowers           int
}

type OpenAIUserAffinityFollowerPoll struct {
	Released              bool
	MayTakeover           bool
	ExpectedLeaderVersion int64
}

// OpenAIUserAffinityDemand 使用账号额度比例表示用户在两个窗口内的预计增量。
type OpenAIUserAffinityDemand struct {
	Demand5H float64
	Demand7D float64
	Version  string
}

// OpenAIUserAffinityIncidentIdentity 将单槽兼容事故与多槽会话事故收敛为同一强类型键。
type OpenAIUserAffinityIncidentIdentity struct {
	UserID              int64
	AccountID           int64
	ScopeKey            string
	PlacementGeneration int64
	ConversationHash    string
	ResidentSlotID      int64
	SlotGeneration      int64
}

// Generation 返回事故在当前调度模式下用于 CAS 的 generation。
func (i OpenAIUserAffinityIncidentIdentity) Generation() int64 {
	if i.SlotGeneration > 0 {
		return i.SlotGeneration
	}
	return i.PlacementGeneration
}

const (
	OpenAIUserResidentSlotStatusProvisional        = "provisional"
	OpenAIUserResidentSlotStatusActive             = "active"
	OpenAIUserResidentSlotStatusReplacementPending = "replacement_pending"
	OpenAIUserResidentSlotStatusDraining           = "draining"
	OpenAIUserResidentSlotStatusExpired            = "expired"
	OpenAIUserResidentSlotStatusReset              = "reset"
)

// OpenAIUserResidentSlot 是用户在单个 scope 内的常驻账号槽位。
type OpenAIUserResidentSlot struct {
	ID                      int64      `json:"id"`
	UserID                  int64      `json:"user_id"`
	ScopeKey                string     `json:"scope_key"`
	SlotIndex               int        `json:"slot_index"`
	AccountID               int64      `json:"account_id"`
	Generation              int64      `json:"generation"`
	Status                  string     `json:"status"`
	AdmittedAt              time.Time  `json:"admitted_at"`
	LastSuccessAt           *time.Time `json:"last_success_at"`
	ExpiresAt               time.Time  `json:"expires_at"`
	UsageScore              float64    `json:"usage_score"`
	ActiveRouteUserCount    int        `json:"active_route_user_count"`
	SoftOwnerUserID         int64      `json:"soft_owner_user_id"`
	ScoreUpdatedAt          time.Time  `json:"score_updated_at"`
	ReplacementSourceSlotID *int64     `json:"replacement_source_slot_id"`
	ConfigVersion           int64      `json:"config_version"`
	ProvisionalToken        string     `json:"-"`
}

// OpenAIUserActiveRoute 是用户在单个 scope 下供新会话复用的唯一短期活动账号。
type OpenAIUserActiveRoute struct {
	UserID                int64      `json:"user_id"`
	ScopeKey              string     `json:"scope_key"`
	ResidentSlotID        int64      `json:"resident_slot_id"`
	AccountID             int64      `json:"account_id"`
	SlotGeneration        int64      `json:"slot_generation"`
	ClaimedAt             *time.Time `json:"claimed_at"`
	ActiveUntil           *time.Time `json:"active_until"`
	PendingResidentSlotID int64      `json:"pending_resident_slot_id"`
	PendingAccountID      int64      `json:"pending_account_id"`
	PendingSlotGeneration int64      `json:"pending_slot_generation"`
	PendingClaimedAt      *time.Time `json:"pending_claimed_at"`
	PendingExpiresAt      *time.Time `json:"pending_expires_at"`
}

// OpenAIAccountSoftOccupancy 描述账号当前活动用户数及稳定软驻留主用户。
type OpenAIAccountSoftOccupancy struct {
	AccountID       int64 `json:"account_id"`
	ActiveUserCount int   `json:"active_user_count"`
	OwnerUserID     int64 `json:"owner_user_id"`
}

// OpenAIUserConversationBinding 将一个逻辑会话固定到其首次成功使用的账号。
type OpenAIUserConversationBinding struct {
	ID                   int64      `json:"id"`
	UserID               int64      `json:"user_id"`
	APIKeyID             int64      `json:"api_key_id"`
	ScopeKey             string     `json:"scope_key"`
	ConversationHash     string     `json:"conversation_hash"`
	ResidentSlotID       int64      `json:"resident_slot_id"`
	AccountID            int64      `json:"account_id"`
	SlotGeneration       int64      `json:"slot_generation"`
	Status               string     `json:"status"`
	ContextRebuildable   bool       `json:"context_rebuildable"`
	FirstOutputCommitted bool       `json:"first_output_committed"`
	ActiveUntil          *time.Time `json:"active_until"`
	ExpiresAt            time.Time  `json:"expires_at"`
	LastSuccessAt        *time.Time `json:"last_success_at"`
	ProvisionalToken     string     `json:"-"`
	ManageActiveRoute    bool       `json:"-"`
	ActiveRoutePending   bool       `json:"-"`
}

// OpenAIUserConversationAlias 是会话绑定的作用域化派生索引，不保存客户端原始标识。
type OpenAIUserConversationAlias struct {
	ScopeKey string
	Type     string
	Hash     string
}

// OpenAIUserConversationReservation 是选号后、上游首输出前的原子会话预留输入。
type OpenAIUserConversationReservation struct {
	UserID                  int64
	APIKeyID                int64
	ScopeKey                string
	ConversationHash        string
	AliasType               string
	AliasHash               string
	Aliases                 []OpenAIUserConversationAlias
	AccountID               int64
	PlacementGeneration     int64
	PreferredResidentSlotID int64
	PreferredSlotGeneration int64
	MaxResidentSlots        int
	ContextRebuildable      bool
	ProvisionalToken        string
	ManageActiveRoute       bool
	Config                  OpenAIUserAffinityConfig
}

// OpenAIUserConversationTransition 以 binding、账号和 token 限定提交或回滚目标。
type OpenAIUserConversationTransition struct {
	BindingID          int64
	UserID             int64
	APIKeyID           int64
	ScopeKey           string
	ConversationHash   string
	ResidentSlotID     int64
	AccountID          int64
	SlotGeneration     int64
	ProvisionalToken   string
	Failover           bool
	SourceAccountID    int64
	SourceSlotID       int64
	SourceGeneration   int64
	Replacement        bool
	ReplacementSlotID  int64
	DetachSource       bool
	ResponseAliasHash  string
	ManageActiveRoute  bool
	ActiveRoutePending bool
	Config             OpenAIUserAffinityConfig
}

// OpenAIUserConversationFailoverReservation 描述一个不破坏原绑定的槽位内重放预留。
type OpenAIUserConversationFailoverReservation struct {
	BindingID            int64
	UserID               int64
	ScopeKey             string
	ConversationHash     string
	SourceAccountID      int64
	SourceResidentSlotID int64
	SourceSlotGeneration int64
	TargetAccountID      int64
	TargetResidentSlotID int64
	TargetSlotGeneration int64
	ProvisionalToken     string
	DetachSource         bool
	Config               OpenAIUserAffinityConfig
}

// OpenAIUserResidentSlotVersion 是替换事务必须重新校验的活动槽位快照。
type OpenAIUserResidentSlotVersion struct {
	ID         int64
	AccountID  int64
	Generation int64
}

// OpenAIUserResidentSlotReplacementReservation 在全槽位失败后预留 BestFit target。
type OpenAIUserResidentSlotReplacementReservation struct {
	BindingID            int64
	UserID               int64
	ScopeKey             string
	ConversationHash     string
	SourceAccountID      int64
	SourceResidentSlotID int64
	SourceSlotGeneration int64
	VictimSlotID         int64
	TargetAccountID      int64
	CheckedSlots         []OpenAIUserResidentSlotVersion
	ProvisionalToken     string
	Config               OpenAIUserAffinityConfig
}

// OpenAIUserPlacement 是常驻槽位首选账号的兼容投影。
type OpenAIUserPlacement struct {
	UserID                    int64      `json:"user_id"`
	ScopeKey                  string     `json:"scope_key"`
	AccountID                 *int64     `json:"account_id"`
	Generation                int64      `json:"generation"`
	Status                    string     `json:"status"`
	AssignedAt                time.Time  `json:"assigned_at"`
	LastActiveAt              *time.Time `json:"last_active_at"`
	ExpiresAt                 time.Time  `json:"expires_at"`
	LastMovedAt               *time.Time `json:"last_moved_at"`
	AssignmentReason          string     `json:"assignment_reason"`
	ResetExcludeSourceAccount *bool      `json:"reset_exclude_source_account"`
	ResetSourceAccountID      *int64     `json:"reset_source_account_id"`
	ResetAt                   *time.Time `json:"reset_at"`
	ResetByAdminID            *int64     `json:"reset_by_admin_id"`
	ResetReason               string     `json:"reset_reason"`
	Predicted5HDemand         *float64   `json:"predicted_5h_demand"`
	Predicted7DDemand         *float64   `json:"predicted_7d_demand"`
	PredictionVersion         string     `json:"prediction_version"`
	ProvisionalToken          string     `json:"-"`
}

// OpenAIUserAffinityProvisionalTransition 冻结归属写入前后的状态，供失败路径恢复。
type OpenAIUserAffinityProvisionalTransition struct {
	Kind              string
	Token             string
	TargetPlacement   OpenAIUserPlacement
	PreviousPlacement *OpenAIUserPlacement
	Config            OpenAIUserAffinityConfig
}

// OpenAIUserPlacementEvent 是管理员反查和搬迁审计共用的事件投影。
type OpenAIUserPlacementEvent struct {
	UserID                       int64
	ScopeKey                     string
	PlacementGeneration          int64
	SourceAccountID              *int64
	TargetAccountID              *int64
	EventType                    string
	Reason                       string
	ConfigVersion                int64
	AccountAffinityConfigVersion int64
	EffectiveSource              string
	ActorAdminID                 *int64
	ResidentSlotID               *int64
}
