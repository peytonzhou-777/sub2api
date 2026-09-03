package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	codexRouteDetectionModel         = "gpt-5.6-sol"
	codexRouteDetectionEffort        = "high"
	codexRouteDetectionPrompt        = "Reply with exactly OK."
	codexRouteDetectionTimeout       = 30 * time.Second
	codexRouteDetectionTimingGrace   = time.Second
	codexRouteDetectionMaxConcurrent = 2
	codexRouteDetectionLockTTL       = time.Minute
)

var (
	ErrCodexRouteDetectionUnsupported = errors.New("route detection only supports OpenAI OAuth accounts")
	ErrCodexRouteDetectionBusy        = errors.New("route detection is already running for this credential account")
	codexSolEnginePattern             = regexp.MustCompile(`^gpt56sol-codex(?:-|$)`)
	codexLunaEnginePattern            = regexp.MustCompile(`^gpt56lun-codex(?:-|$)`)
)

var codexRouteDetectionHeaderNames = [...]string{
	"x-codex-primary-used-percent",
	"x-codex-primary-window-minutes",
	"x-codex-active-limit",
	"x-codex-safety-buffering-faster-model",
}

type CodexRouteDetectionResult struct {
	AccountID           int64             `json:"account_id"`
	CredentialAccountID int64             `json:"credential_account_id"`
	Status              string            `json:"status"`
	CheckedAt           time.Time         `json:"checked_at"`
	ReasonCode          string            `json:"reason_code"`
	RequestedModel      string            `json:"requested_model"`
	ReportedModel       string            `json:"reported_model,omitempty"`
	ResponseHeaders     map[string]string `json:"response_headers"`
}

// DetectOpenAIOAuthRoute 使用账号的 strict Persona 发起独立 WS 请求，并依据 timing engine_ids 自动判定。
func (s *AccountTestService) DetectOpenAIOAuthRoute(ctx context.Context, accountID int64) (*CodexRouteDetectionResult, error) {
	if s == nil || s.accountRepo == nil {
		return nil, errors.New("account repository is not configured")
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil || !account.IsOpenAIOAuth() {
		return nil, ErrCodexRouteDetectionUnsupported
	}

	credentialAccount := account
	if account.IsCredentialShadow() {
		credentialAccount, err = resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return nil, err
		}
	}
	if credentialAccount == nil || !credentialAccount.IsOpenAIOAuth() || credentialAccount.IsOpenAIAgentIdentity() {
		return nil, ErrCodexRouteDetectionUnsupported
	}

	release, err := s.acquireCodexRouteDetection(ctx, credentialAccount.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	distributedRelease, err := s.acquireCodexRouteDetectionLease(ctx, credentialAccount.ID)
	if err != nil {
		return nil, err
	}
	defer distributedRelease()

	result := &CodexRouteDetectionResult{
		AccountID:           account.ID,
		CredentialAccountID: credentialAccount.ID,
		RequestedModel:      codexRouteDetectionModel,
		ResponseHeaders:     emptyCodexRouteDetectionHeaders(),
	}
	probeCtx, cancel := context.WithTimeout(ctx, codexRouteDetectionTimeout)
	defer cancel()

	s.runCodexRouteDetection(probeCtx, credentialAccount, result)
	result.CheckedAt = time.Now().UTC()

	snapshot := map[string]any{
		"status":                result.Status,
		"checked_at":            result.CheckedAt.Format(time.RFC3339Nano),
		"reason_code":           result.ReasonCode,
		"requested_model":       result.RequestedModel,
		"reported_model":        result.ReportedModel,
		"credential_account_id": result.CredentialAccountID,
		"response_headers":      result.ResponseHeaders,
	}
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{"codex_route_detection": snapshot}); err != nil {
		return nil, fmt.Errorf("save route detection: %w", err)
	}
	return result, nil
}

func (s *AccountTestService) acquireCodexRouteDetectionLease(ctx context.Context, credentialAccountID int64) (func(), error) {
	if s.codexRouteDetectionLock == nil {
		return func() {}, nil
	}
	owner := uuid.NewString()
	key := fmt.Sprintf("codex-route-detection:%d", credentialAccountID)
	acquired, err := s.codexRouteDetectionLock.TryAcquireLeaderLock(ctx, key, owner, codexRouteDetectionLockTTL)
	if err != nil {
		return nil, fmt.Errorf("acquire route detection lease: %w", err)
	}
	if !acquired {
		return nil, ErrCodexRouteDetectionBusy
	}
	return func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.codexRouteDetectionLock.ReleaseLeaderLock(releaseCtx, key, owner)
	}, nil
}

func (s *AccountTestService) acquireCodexRouteDetection(ctx context.Context, credentialAccountID int64) (func(), error) {
	if _, loaded := s.codexRouteDetectionActive.LoadOrStore(credentialAccountID, struct{}{}); loaded {
		return nil, ErrCodexRouteDetectionBusy
	}
	s.codexRouteDetectionOnce.Do(func() {
		s.codexRouteDetectionSlots = make(chan struct{}, codexRouteDetectionMaxConcurrent)
	})
	select {
	case s.codexRouteDetectionSlots <- struct{}{}:
		return func() {
			<-s.codexRouteDetectionSlots
			s.codexRouteDetectionActive.Delete(credentialAccountID)
		}, nil
	case <-ctx.Done():
		s.codexRouteDetectionActive.Delete(credentialAccountID)
		return nil, ctx.Err()
	}
}

func (s *AccountTestService) runCodexRouteDetection(ctx context.Context, account *Account, result *CodexRouteDetectionResult) {
	gateway := s.openAIGatewayService
	if gateway == nil {
		setCodexRouteDetectionError(result, "gateway_unavailable")
		return
	}

	target, err := s.resolveCodexRouteDetectionTarget(ctx, account)
	if err != nil {
		setCodexRouteDetectionError(result, "strict_persona_unavailable")
		return
	}
	ctx = ContextWithOpenAIExecutionTarget(ctx, target)

	basePayload := createOpenAITestPayload(
		codexRouteDetectionModel,
		true,
		codexRouteDetectionPrompt,
		codexRouteDetectionEffort,
	)
	payload := gateway.buildOpenAIWSCreatePayload(basePayload, account)
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		setCodexRouteDetectionError(result, "payload_invalid")
		return
	}

	probeRecorder := httptest.NewRecorder()
	probeGin, _ := gin.CreateTestContext(probeRecorder)
	probeGin.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(rawPayload)).WithContext(ctx)
	SetOpenAIClientTransport(probeGin, OpenAIClientTransportWS)
	strictPayload, err := gateway.applyCodexFingerprintRawForAttempt(ctx, probeGin, account, rawPayload, true)
	if err != nil {
		setCodexRouteDetectionError(result, "fingerprint_prepare_failed")
		return
	}

	token, _, err := gateway.GetAccessTokenForRequest(ctx, probeGin, account)
	if err != nil {
		setCodexRouteDetectionError(result, "credential_unavailable")
		return
	}
	decision := OpenAIWSProtocolDecision{
		Transport: OpenAIUpstreamTransportResponsesWebsocketV2,
		Reason:    "admin_route_detection",
	}
	headers, _, err := gateway.buildOpenAIWSHeaders(
		ctx,
		probeGin,
		account,
		token,
		decision,
		true,
		"",
		"",
		"",
		codexRouteDetectionModel,
		"",
	)
	if err != nil {
		setCodexRouteDetectionError(result, "headers_prepare_failed")
		return
	}
	wsURL, err := gateway.buildOpenAIResponsesWSURL(account)
	if err != nil {
		setCodexRouteDetectionError(result, "url_prepare_failed")
		return
	}

	dialer := s.codexRouteWSDialer
	if dialer == nil {
		dialer = gateway.getOpenAIWSPassthroughDialer()
	}
	if dialer == nil {
		setCodexRouteDetectionError(result, "dialer_unavailable")
		return
	}
	conn, _, handshakeHeaders, err := dialer.Dial(ctx, wsURL, headers, resolveOpenAIUpstreamProxyURL(ctx, account))
	result.ResponseHeaders = selectCodexRouteDetectionHeaders(handshakeHeaders)
	if err != nil {
		setCodexRouteDetectionError(result, "websocket_handshake_failed")
		return
	}
	if conn == nil {
		setCodexRouteDetectionError(result, "websocket_connection_unavailable")
		return
	}
	defer func() { _ = conn.Close() }()

	var outboundPayload map[string]any
	if err := json.Unmarshal(strictPayload, &outboundPayload); err != nil {
		setCodexRouteDetectionError(result, "payload_invalid")
		return
	}
	if err := conn.WriteJSON(ctx, outboundPayload); err != nil {
		setCodexRouteDetectionError(result, "websocket_write_failed")
		return
	}
	s.readCodexRouteDetectionEvents(ctx, conn, result)
}

func (s *AccountTestService) resolveCodexRouteDetectionTarget(ctx context.Context, account *Account) (OpenAIExecutionTarget, error) {
	repo, ok := s.accountRepo.(OpenAIAccountPersonaRepository)
	if !ok {
		return OpenAIExecutionTarget{}, ErrOpenAIAccountPersonaNotFound
	}
	personas, err := repo.ListAccountPersonas(ctx, account.ID)
	if err != nil {
		return OpenAIExecutionTarget{}, err
	}
	for _, persona := range personas {
		if persona.Position != 0 || persona.ProfileID != SessionPersonaCodexCLIStrict || !persona.AcceptsNewRoot() {
			continue
		}
		session, sessionErr := repo.GetAccountPersonaSession(ctx, account.ID, persona.ID, persona.CurrentSessionEpoch, time.Now().UTC())
		if sessionErr != nil {
			return OpenAIExecutionTarget{}, sessionErr
		}
		return OpenAIExecutionTargetFromPersonaSession(persona, *session)
	}
	return OpenAIExecutionTarget{}, ErrOpenAIAccountPersonaNotFound
}

func (s *AccountTestService) readCodexRouteDetectionEvents(ctx context.Context, conn openAIWSClientConn, result *CodexRouteDetectionResult) {
	var engineIDs []string
	timingSeen := false
	completed := false
	for {
		readCtx := ctx
		var cancel context.CancelFunc
		if completed && !timingSeen {
			readCtx, cancel = context.WithTimeout(ctx, codexRouteDetectionTimingGrace)
		}
		payload, err := conn.ReadMessage(readCtx)
		if cancel != nil {
			cancel()
		}
		if err != nil {
			if completed {
				result.Status, result.ReasonCode = classifyCodexRouteEngineIDs(timingSeen, engineIDs)
				return
			}
			setCodexRouteDetectionError(result, "websocket_read_failed")
			return
		}

		eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
		switch eventType {
		case "responsesapi.websocket_timing":
			timingSeen = true
			engineIDs = append(engineIDs, codexTimingEngineIDs(payload)...)
		case "response.completed":
			completed = true
			result.ReportedModel = strings.TrimSpace(gjson.GetBytes(payload, "response.model").String())
		case "response.failed", "response.incomplete", "error":
			setCodexRouteDetectionError(result, "upstream_terminal_error")
			return
		}
		if completed && timingSeen {
			result.Status, result.ReasonCode = classifyCodexRouteEngineIDs(true, engineIDs)
			return
		}
	}
}

func codexTimingEngineIDs(payload []byte) []string {
	values := gjson.GetBytes(payload, "timing_metrics.engine_ids")
	if !values.Exists() {
		values = gjson.GetBytes(payload, "data.timing_metrics.engine_ids")
	}
	result := make([]string, 0, len(values.Array()))
	for _, value := range values.Array() {
		if engineID := strings.TrimSpace(value.String()); engineID != "" {
			result = append(result, engineID)
		}
	}
	return result
}

func classifyCodexRouteEngineIDs(timingSeen bool, engineIDs []string) (string, string) {
	if !timingSeen || len(engineIDs) == 0 {
		return "inconclusive", "timing_missing"
	}
	hasSol := false
	hasLuna := false
	for _, engineID := range engineIDs {
		switch {
		case codexSolEnginePattern.MatchString(engineID):
			hasSol = true
		case codexLunaEnginePattern.MatchString(engineID):
			hasLuna = true
		default:
			return "inconclusive", "unknown_engine"
		}
	}
	if hasSol && hasLuna {
		return "inconclusive", "mixed_engines"
	}
	if hasSol {
		return "sol", "sol_engine"
	}
	if hasLuna {
		return "luna", "luna_engine"
	}
	return "inconclusive", "timing_missing"
}

func setCodexRouteDetectionError(result *CodexRouteDetectionResult, reason string) {
	result.Status = "error"
	result.ReasonCode = reason
}

func emptyCodexRouteDetectionHeaders() map[string]string {
	result := make(map[string]string, len(codexRouteDetectionHeaderNames))
	for _, name := range codexRouteDetectionHeaderNames {
		result[name] = ""
	}
	return result
}

func selectCodexRouteDetectionHeaders(headers http.Header) map[string]string {
	result := emptyCodexRouteDetectionHeaders()
	for _, name := range codexRouteDetectionHeaderNames {
		result[name] = strings.Join(headers.Values(name), ", ")
	}
	return result
}
