package service

import (
	"context"
	"time"
)

// OpenAIUserAffinityStore 是账号仓储可选的用户粘性状态能力。
// 通过可选接口接入，保持现有测试替身和非 OpenAI 调度调用方兼容。
type OpenAIUserAffinityStore interface {
	GetOpenAIUserPlacement(ctx context.Context, userID int64, scopeKey string) (*OpenAIUserPlacement, error)
	UpsertOpenAIUserPlacement(ctx context.Context, placement OpenAIUserPlacement) error
	RecordOpenAIUserPlacementEvent(ctx context.Context, event OpenAIUserPlacementEvent) error
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
	ConfirmOpenAIUserAffinitySuccess(ctx context.Context, userID, accountID, generation int64, scopeKey string) error
	RollbackOpenAIUserAffinityPlacement(ctx context.Context, transition OpenAIUserAffinityProvisionalTransition, config OpenAIUserAffinityConfig) (bool, error)
	RecordOpenAIUserAffinityCapacityFailure(ctx context.Context, userID, accountID, generation int64, scopeKey, requestIDHash, reason string, config OpenAIUserAffinityConfig) (*time.Time, error)
	GetOpenAIUserAffinityMigrationAuthorizedAt(ctx context.Context, userID, accountID, generation int64, scopeKey string) (*time.Time, error)
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
	ConfirmOpenAIUserAffinitySuccess(ctx context.Context, userID, accountID, generation int64, scopeKey string) error
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

// OpenAIUserPlacement 是 14 天滑动居住归属的服务层投影。
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
}
