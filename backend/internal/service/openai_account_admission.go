package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

type OpenAIAdmissionClass string

const (
	OpenAIAdmissionInteractive OpenAIAdmissionClass = "interactive"
	OpenAIAdmissionBackground  OpenAIAdmissionClass = "background"
)

var (
	ErrOpenAIAdmissionQueueFull  = errors.New("openai account admission queue is full")
	ErrOpenAIAdmissionTicketGone = errors.New("openai account admission ticket is no longer queued")
)

// OpenAIAccountAdmissionTicket 只携带准入元数据，严禁在 Redis 队列保存请求正文。
type OpenAIAccountAdmissionTicket struct {
	ID        string
	AccountID int64
	// Persona/slot metadata is carried with the ticket so admission state can
	// be isolated without putting request bodies into Redis. Empty Persona
	// preserves the legacy account/session key layout.
	Persona           string
	SlotID            int
	SlotGeneration    int64
	SlotSetGeneration int64
	CredentialChainID string
	SessionScopeHash  string
	SessionEpoch      int64
	Class             OpenAIAdmissionClass
	EnqueuedAt        time.Time
	Deadline          time.Time
	EstimatedTokens   int64
}

type OpenAIAccountAdmissionPoll struct {
	Selected bool
	Delay    time.Duration
}

type OpenAIAccountAdmissionGrant struct {
	Granted bool
	Delay   time.Duration
}

// OpenAIAccountAdmissionQueue 是 Redis 跨实例账号队列的服务层端口。
type OpenAIAccountAdmissionQueue interface {
	Enqueue(context.Context, OpenAIAccountAdmissionTicket, OpenAIAccountAdmissionConfig) error
	Poll(context.Context, OpenAIAccountAdmissionTicket, OpenAIAccountAdmissionConfig) (OpenAIAccountAdmissionPoll, error)
	Grant(context.Context, OpenAIAccountAdmissionTicket, OpenAIAccountAdmissionConfig, time.Duration) (OpenAIAccountAdmissionGrant, error)
	Remove(context.Context, OpenAIAccountAdmissionTicket) error
}

type OpenAIAccountAdmissionRequest struct {
	AccountID int64
	// Persona/slot metadata is copied into the queue ticket. CredentialChainID
	// is retained for audit/correlation; key partitioning intentionally follows
	// Persona and slot generations rather than the token chain.
	Persona           string
	SlotID            int
	SlotGeneration    int64
	SlotSetGeneration int64
	CredentialChainID string
	SessionScopeHash  string
	SessionEpoch      int64
	Class             OpenAIAdmissionClass
	EstimatedTokens   int64
	MaxConcurrency    int
	TryAcquireSlot    func(context.Context, int64, int) (func(), bool, error)
}

type OpenAIAccountAdmissionResult struct {
	ReleaseFunc func()
	QueueWait   time.Duration
	Jitter      time.Duration
	Queued      bool
}

type openAIAccountQueueWaitContextKey struct{}

// ContextWithOpenAIAccountQueueWait 把管理员可见的账号排队耗时附着到用量记录上下文。
func ContextWithOpenAIAccountQueueWait(ctx context.Context, wait time.Duration) context.Context {
	if ctx == nil || wait <= 0 {
		return ctx
	}
	return context.WithValue(ctx, openAIAccountQueueWaitContextKey{}, wait.Milliseconds())
}

// OpenAIAccountQueueWaitMSFromContext 读取账号准入排队耗时，未排队时返回 0。
func OpenAIAccountQueueWaitMSFromContext(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	wait, _ := ctx.Value(openAIAccountQueueWaitContextKey{}).(int64)
	return openAIAdmissionMaxInt64(wait, 0)
}

type OpenAIAccountAdmissionErrorKind string

const (
	OpenAIAdmissionErrorQueueDisabled OpenAIAccountAdmissionErrorKind = "queue_disabled"
	OpenAIAdmissionErrorQueueFull     OpenAIAccountAdmissionErrorKind = "queue_full"
	OpenAIAdmissionErrorTimeout       OpenAIAccountAdmissionErrorKind = "queue_timeout"
	OpenAIAdmissionErrorUnavailable   OpenAIAccountAdmissionErrorKind = "coordination_unavailable"
)

// OpenAIAccountAdmissionError 为 handler 提供稳定的 HTTP 和记录分类。
type OpenAIAccountAdmissionError struct {
	Kind       OpenAIAccountAdmissionErrorKind
	StatusCode int
	Wait       time.Duration
	RetryAfter time.Duration
	Cause      error
}

func (e *OpenAIAccountAdmissionError) Error() string {
	if e == nil {
		return "openai account admission failed"
	}
	return fmt.Sprintf("openai account admission %s after %s", e.Kind, e.Wait)
}

func (e *OpenAIAccountAdmissionError) Unwrap() error { return e.Cause }

// OpenAIAccountAdmissionService 在账号确定后统一协调 RPM、TPM 与账号并发。
type OpenAIAccountAdmissionService struct {
	settings *SettingService
	queue    OpenAIAccountAdmissionQueue
	now      func() time.Time
	jitter   func(int, int) time.Duration
}

func NewOpenAIAccountAdmissionService(settings *SettingService, queue OpenAIAccountAdmissionQueue) *OpenAIAccountAdmissionService {
	return &OpenAIAccountAdmissionService{
		settings: settings,
		queue:    queue,
		now:      time.Now,
		jitter: func(minMS, maxMS int) time.Duration {
			if maxMS <= minMS {
				return time.Duration(minMS) * time.Millisecond
			}
			return time.Duration(minMS+rand.Intn(maxMS-minMS+1)) * time.Millisecond
		},
	}
}

// Config 返回本次请求应固化的配置快照。
func (s *OpenAIAccountAdmissionService) Config(ctx context.Context) (OpenAIAccountAdmissionConfig, error) {
	if s == nil || s.settings == nil {
		return DefaultOpenAIAccountAdmissionConfig(), nil
	}
	return s.settings.GetOpenAIAccountAdmissionConfig(ctx)
}

// Acquire 使用同一截止时间完成排队、限额许可、并发抢槽和最终随机抖动。
func (s *OpenAIAccountAdmissionService) Acquire(ctx context.Context, req OpenAIAccountAdmissionRequest, cfg OpenAIAccountAdmissionConfig) (OpenAIAccountAdmissionResult, error) {
	started := s.now()
	deadline := started.Add(time.Duration(cfg.MaxWaitSeconds) * time.Second)
	ticket := OpenAIAccountAdmissionTicket{
		ID: uuid.NewString(), AccountID: req.AccountID,
		Persona:           strings.ToLower(strings.TrimSpace(req.Persona)),
		SlotID:            req.SlotID,
		SlotGeneration:    req.SlotGeneration,
		SlotSetGeneration: req.SlotSetGeneration, CredentialChainID: req.CredentialChainID,
		SessionScopeHash: strings.ToLower(strings.TrimSpace(req.SessionScopeHash)), SessionEpoch: req.SessionEpoch,
		Class:      req.Class,
		EnqueuedAt: started, Deadline: deadline, EstimatedTokens: openAIAdmissionMaxInt64(req.EstimatedTokens, 1),
	}
	if ticket.Class != OpenAIAdmissionBackground {
		ticket.Class = OpenAIAdmissionInteractive
	}
	if s.queue == nil || req.TryAcquireSlot == nil {
		return OpenAIAccountAdmissionResult{}, &OpenAIAccountAdmissionError{Kind: OpenAIAdmissionErrorUnavailable, StatusCode: http.StatusServiceUnavailable, Cause: errors.New("admission dependencies unavailable")}
	}
	if err := s.queue.Enqueue(ctx, ticket, cfg); err != nil {
		kind, status := OpenAIAdmissionErrorUnavailable, http.StatusServiceUnavailable
		retryAfter := time.Duration(0)
		if errors.Is(err, ErrOpenAIAdmissionQueueFull) {
			kind, status = OpenAIAdmissionErrorQueueFull, http.StatusServiceUnavailable
			retryAfter = time.Second
		}
		return OpenAIAccountAdmissionResult{}, &OpenAIAccountAdmissionError{Kind: kind, StatusCode: status, Wait: s.now().Sub(started), RetryAfter: retryAfter, Cause: err}
	}
	defer func() { _ = s.queue.Remove(context.WithoutCancel(ctx), ticket) }()

	waited := false
	retryAfter := time.Second
	for {
		now := s.now()
		if !now.Before(deadline) {
			return OpenAIAccountAdmissionResult{}, &OpenAIAccountAdmissionError{Kind: OpenAIAdmissionErrorTimeout, StatusCode: http.StatusServiceUnavailable, Wait: now.Sub(started), RetryAfter: retryAfter}
		}
		poll, err := s.queue.Poll(ctx, ticket, cfg)
		if err != nil {
			if errors.Is(err, ErrOpenAIAdmissionTicketGone) {
				return OpenAIAccountAdmissionResult{}, &OpenAIAccountAdmissionError{Kind: OpenAIAdmissionErrorTimeout, StatusCode: http.StatusServiceUnavailable, Wait: s.now().Sub(started), RetryAfter: retryAfter, Cause: err}
			}
			return OpenAIAccountAdmissionResult{}, &OpenAIAccountAdmissionError{Kind: OpenAIAdmissionErrorUnavailable, StatusCode: http.StatusServiceUnavailable, Wait: s.now().Sub(started), Cause: err}
		}
		if !poll.Selected || poll.Delay > 0 {
			waited = true
			if poll.Delay > 0 {
				retryAfter = poll.Delay
			}
			if !cfg.QueueEnabled {
				return OpenAIAccountAdmissionResult{}, &OpenAIAccountAdmissionError{Kind: OpenAIAdmissionErrorQueueDisabled, StatusCode: http.StatusServiceUnavailable, Wait: s.now().Sub(started), RetryAfter: retryAfter}
			}
			if err := waitOpenAIAdmission(ctx, deadline, poll.Delay); err != nil {
				return OpenAIAccountAdmissionResult{}, &OpenAIAccountAdmissionError{Kind: OpenAIAdmissionErrorTimeout, StatusCode: http.StatusServiceUnavailable, Wait: s.now().Sub(started), RetryAfter: retryAfter, Cause: err}
			}
			continue
		}

		release, acquired, err := req.TryAcquireSlot(ctx, req.AccountID, req.MaxConcurrency)
		if err != nil {
			return OpenAIAccountAdmissionResult{}, &OpenAIAccountAdmissionError{Kind: OpenAIAdmissionErrorUnavailable, StatusCode: http.StatusServiceUnavailable, Wait: s.now().Sub(started), Cause: err}
		}
		if !acquired {
			waited = true
			retryAfter = time.Second
			if !cfg.QueueEnabled {
				return OpenAIAccountAdmissionResult{}, &OpenAIAccountAdmissionError{Kind: OpenAIAdmissionErrorQueueDisabled, StatusCode: http.StatusServiceUnavailable, Wait: s.now().Sub(started), RetryAfter: retryAfter}
			}
			if err := waitOpenAIAdmission(ctx, deadline, 25*time.Millisecond); err != nil {
				return OpenAIAccountAdmissionResult{}, &OpenAIAccountAdmissionError{Kind: OpenAIAdmissionErrorTimeout, StatusCode: http.StatusServiceUnavailable, Wait: s.now().Sub(started), RetryAfter: retryAfter, Cause: err}
			}
			continue
		}

		jitter := time.Duration(0)
		if waited {
			jitter = s.jitter(cfg.JitterMinMS, cfg.JitterMaxMS)
		}
		grant, err := s.queue.Grant(ctx, ticket, cfg, jitter)
		if err != nil {
			release()
			if errors.Is(err, ErrOpenAIAdmissionTicketGone) {
				return OpenAIAccountAdmissionResult{}, &OpenAIAccountAdmissionError{Kind: OpenAIAdmissionErrorTimeout, StatusCode: http.StatusServiceUnavailable, Wait: s.now().Sub(started), RetryAfter: retryAfter, Cause: err}
			}
			return OpenAIAccountAdmissionResult{}, &OpenAIAccountAdmissionError{Kind: OpenAIAdmissionErrorUnavailable, StatusCode: http.StatusServiceUnavailable, Wait: s.now().Sub(started), Cause: err}
		}
		if !grant.Granted {
			release()
			waited = true
			if grant.Delay > 0 {
				retryAfter = grant.Delay
			}
			if !cfg.QueueEnabled {
				return OpenAIAccountAdmissionResult{}, &OpenAIAccountAdmissionError{Kind: OpenAIAdmissionErrorQueueDisabled, StatusCode: http.StatusServiceUnavailable, Wait: s.now().Sub(started), RetryAfter: retryAfter}
			}
			if err := waitOpenAIAdmission(ctx, deadline, grant.Delay); err != nil {
				return OpenAIAccountAdmissionResult{}, &OpenAIAccountAdmissionError{Kind: OpenAIAdmissionErrorTimeout, StatusCode: http.StatusServiceUnavailable, Wait: s.now().Sub(started), RetryAfter: retryAfter, Cause: err}
			}
			continue
		}
		if grant.Delay > 0 {
			if err := waitOpenAIAdmissionDuration(ctx, deadline, grant.Delay); err != nil {
				release()
				return OpenAIAccountAdmissionResult{}, &OpenAIAccountAdmissionError{Kind: OpenAIAdmissionErrorTimeout, StatusCode: http.StatusServiceUnavailable, Wait: s.now().Sub(started), RetryAfter: grant.Delay, Cause: err}
			}
		}
		return OpenAIAccountAdmissionResult{
			ReleaseFunc: release, QueueWait: s.now().Sub(started), Jitter: grant.Delay, Queued: waited,
		}, nil
	}
}

func waitOpenAIAdmission(ctx context.Context, deadline time.Time, requested time.Duration) error {
	if requested < 25*time.Millisecond {
		requested = 25 * time.Millisecond
	}
	if requested > 250*time.Millisecond {
		requested = 250 * time.Millisecond
	}
	return waitOpenAIAdmissionDuration(ctx, deadline, requested)
}

// waitOpenAIAdmissionDuration 保留最终抖动的精确配置值，不套用轮询休眠下限。
func waitOpenAIAdmissionDuration(ctx context.Context, deadline time.Time, requested time.Duration) error {
	if requested <= 0 {
		return nil
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.DeadlineExceeded
	}
	if requested > remaining {
		return context.DeadlineExceeded
	}
	timer := time.NewTimer(requested)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ClassifyOpenAIAdmissionClass 复用 Codex 指纹规则区分主线程与子代理。
func ClassifyOpenAIAdmissionClass(headers http.Header, body []byte) OpenAIAdmissionClass {
	original := extractCodexFingerprintOriginalIDs(headers, body)
	if original.isSubagent {
		return OpenAIAdmissionBackground
	}
	return OpenAIAdmissionInteractive
}

// EstimateOpenAIAdmissionTokens 估算输入，并按模型输入上限及输出上限约束 TPM 预留。
func EstimateOpenAIAdmissionTokens(body []byte, defaultOutput, maxInput, maxOutput int64) int64 {
	var req openAIInputTokensCountRequest
	if err := jsonUnmarshalOpenAIAdmission(body, &req); err != nil {
		return openAIAdmissionOutputReserve(defaultOutput, defaultOutput, maxOutput)
	}
	// Messages 兼容入口先走项目既有结构化转换，再复用 Responses tokenizer。
	if gjson.GetBytes(body, "messages").Exists() {
		var anthropicReq apicompat.AnthropicRequest
		if err := jsonUnmarshalOpenAIAdmission(body, &anthropicReq); err == nil {
			if responsesReq, err := apicompat.AnthropicToResponses(&anthropicReq); err == nil {
				req = openAIInputTokensCountRequest{
					Model: responsesReq.Model, Instructions: responsesReq.Instructions, Input: responsesReq.Input,
					Tools: responsesReq.Tools, ToolChoice: responsesReq.ToolChoice,
				}
			}
		}
	}
	input, err := estimateOpenAIInputTokens(req)
	if err != nil {
		input = max(int(defaultOutput/4), 1)
	}
	estimatedInput := int64(input)
	if maxInput > 0 && estimatedInput > maxInput {
		estimatedInput = maxInput
	}
	output := defaultOutput
	if value := gjsonGetInt64(body, "max_output_tokens"); value > 0 {
		output = value
	} else if value := gjsonGetInt64(body, "max_tokens"); value > 0 {
		output = value
	}
	return estimatedInput + openAIAdmissionOutputReserve(output, defaultOutput, maxOutput)
}

// EstimateOpenAIAccountAdmissionTokens 在账号确定后按实际转发模型解析输出上限。
func (s *OpenAIGatewayService) EstimateOpenAIAccountAdmissionTokens(account *Account, body []byte, modelHint string, defaultOutput int64) int64 {
	requestedModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if requestedModel == "" {
		requestedModel = strings.TrimSpace(modelHint)
	}
	resolvedModel := normalizeOpenAIModelForUpstream(account, resolveOpenAIForwardModel(account, requestedModel, ""))
	maxInput, maxOutput := int64(0), int64(0)
	if s != nil && s.billingService != nil && s.billingService.pricingService != nil {
		maxInput, maxOutput = s.billingService.pricingService.GetIdentifiedModelTokenLimits(resolvedModel)
	}
	return EstimateOpenAIAdmissionTokens(body, defaultOutput, maxInput, maxOutput)
}

func openAIAdmissionOutputReserve(requested, configured, modelMax int64) int64 {
	configured = openAIAdmissionMaxInt64(configured, 1)
	limit := modelMax
	if limit <= 0 {
		limit = configured
	}
	requested = openAIAdmissionMaxInt64(requested, 1)
	if requested > limit {
		return limit
	}
	return requested
}

func openAIAdmissionMaxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

// 下列小包装把热路径依赖限制在本文件，便于输入结构演进。
var jsonUnmarshalOpenAIAdmission = func(body []byte, target any) error {
	return json.Unmarshal(body, target)
}

var gjsonGetInt64 = func(body []byte, path string) int64 {
	return gjson.GetBytes(body, strings.TrimSpace(path)).Int()
}
