package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type openAIWSRejectedFieldRetryError struct {
	body   []byte
	reason string
}

func (e *openAIWSRejectedFieldRetryError) Error() string {
	if e == nil || strings.TrimSpace(e.reason) == "" {
		return "retry websocket turn after rejected field normalization"
	}
	return "retry websocket turn after rejected field normalization: " + e.reason
}

func openAIWSRejectedFieldRetryHTTPStatus(message []byte) int {
	for _, value := range gjson.GetManyBytes(message, "status", "status_code", "error.status", "error.status_code") {
		status := int(value.Int())
		if status >= 100 && status <= 599 {
			return status
		}
	}
	return openAIWSErrorHTTPStatus(message)
}

func (s *OpenAIGatewayService) openAIWSIngressInterTurnIdleTimeout() time.Duration {
	if s == nil || s.cfg == nil || s.cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(s.cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds) * time.Second
}

// UsesOpenAIResponsesWebSocketPassthrough 判断账号是否走不触发 BeforeTurn 的 WS 直通链路。
func (s *OpenAIGatewayService) UsesOpenAIResponsesWebSocketPassthrough(account *Account) bool {
	if s == nil || s.cfg == nil || account == nil || !s.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled {
		return false
	}
	return account.ResolveOpenAIResponsesWebSocketV2Mode(s.cfg.Gateway.OpenAIWS.IngressModeDefault) == OpenAIWSIngressModePassthrough
}

// newOpenAIWSDownstreamWriteContext binds writes directly to the client
// lifecycle while excluding the separate ingress-lease cancellation signal.
// This lets a lease-loss path finish its current client write before
// ReadOpenAIWSClientMessage sends the retryable close frame.
func newOpenAIWSDownstreamWriteContext(controlCtx context.Context, hooks *OpenAIWSIngressHooks, timeout time.Duration) (context.Context, context.CancelFunc) {
	writeParent := controlCtx
	if hooks != nil && hooks.ClientLifecycleContext != nil {
		writeParent = hooks.ClientLifecycleContext
	}
	if writeParent == nil {
		writeParent = context.Background()
	}
	return context.WithTimeout(writeParent, timeout)
}

func (s *OpenAIGatewayService) ProxyResponsesWebSocketFromClient(
	ctx context.Context,
	c *gin.Context,
	clientConn *coderws.Conn,
	account *Account,
	token string,
	firstClientMessage []byte,
	hooks *OpenAIWSIngressHooks,
) (returnErr error) {
	if s == nil {
		return errors.New("service is nil")
	}
	if c == nil {
		return errors.New("gin context is nil")
	}
	if clientConn == nil {
		return errors.New("client websocket is nil")
	}
	if account == nil {
		return errors.New("account is nil")
	}
	// A handler may reuse the same gin context across account failover attempts.
	// Never let an OAuth attempt's response aliases leak into the next account.
	setCodexToolNameReverse(c, nil)
	if _, err := s.prepareCodexAccountIdentitySource(ctx, c, account); err != nil {
		return err
	}
	if err := validateOpenAIWSBearerToken(account, token); err != nil {
		return err
	}
	restoreAttemptRequest := s.isolateOpenAITurnStateAttempt(ctx, c, account, firstClientMessage)
	defer restoreAttemptRequest()

	// 预取一次 OpenAI Fast Policy settings，绑定到 ctx，让该 WS session
	// 内所有帧的 evaluateOpenAIFastPolicy 调用复用同一份快照，避免每帧
	// 进入 DB / settingRepo。Trade-off 见 withOpenAIFastPolicyContext 注释。
	if s.settingService != nil {
		if settings, err := s.settingService.GetOpenAIFastPolicySettings(ctx); err == nil && settings != nil {
			ctx = withOpenAIFastPolicyContext(ctx, settings)
		}
	}

	// The handler normally owns this registration across retry attempts. Direct
	// callers still get the same session-scoped preemption behavior here.
	if preemptCtx, cleanupPreempt, armed := s.BeginOpenAIWSIngressSessionPreemption(ctx, c, account, firstClientMessage); armed {
		ctx = preemptCtx
		defer cleanupPreempt()
		defer func() {
			if isOpenAIWSSessionPreempted(ctx) {
				returnErr = errOpenAIWSSessionPreempted
			}
		}()
	}

	wsDecision := s.getOpenAIWSProtocolResolver().Resolve(account)
	forceHTTPBridge := account.Platform == PlatformGrok ||
		(s.pluginManager != nil && s.pluginManager.ShouldRouteOpenAIOAuth(account))
	modeRouterV2Enabled := s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled
	ingressMode := OpenAIWSIngressModeCtxPool
	if modeRouterV2Enabled && !forceHTTPBridge {
		ingressMode = account.ResolveOpenAIResponsesWebSocketV2Mode(s.cfg.Gateway.OpenAIWS.IngressModeDefault)
		if ingressMode == OpenAIWSIngressModeOff {
			return NewOpenAIWSClientCloseError(
				coderws.StatusPolicyViolation,
				"websocket mode is disabled for this account",
				nil,
			)
		}
		switch ingressMode {
		case OpenAIWSIngressModePassthrough:
			if wsDecision.Transport != OpenAIUpstreamTransportResponsesWebsocketV2 {
				return fmt.Errorf("websocket ingress requires ws_v2 transport, got=%s", wsDecision.Transport)
			}
			// 透传 relay 通过 TurnStarted 记录每个 turn 的开始时刻，但不触发
			// BeforeTurn；因此仍只有建连时的利润准入门，没有 turn 级复核。
			// handler 计费在 turn 定价未冻结时回退到对应的 turn 开始时刻。
			return s.proxyResponsesWebSocketV2Passthrough(
				ctx,
				c,
				clientConn,
				account,
				token,
				firstClientMessage,
				hooks,
				wsDecision,
			)
		case OpenAIWSIngressModeHTTPBridge:
			forceHTTPBridge = true
		case OpenAIWSIngressModeCtxPool, OpenAIWSIngressModeShared, OpenAIWSIngressModeDedicated:
			// continue
		default:
			return NewOpenAIWSClientCloseError(
				coderws.StatusPolicyViolation,
				"websocket mode only supports ctx_pool/passthrough/http_bridge",
				nil,
			)
		}
	}
	if !forceHTTPBridge && wsDecision.Transport != OpenAIUpstreamTransportResponsesWebsocketV2 {
		return fmt.Errorf("websocket ingress requires ws_v2 transport, got=%s", wsDecision.Transport)
	}
	dedicatedMode := modeRouterV2Enabled && ingressMode == OpenAIWSIngressModeDedicated

	wsURL := ""
	wsHost := "-"
	wsPath := "-"
	if forceHTTPBridge {
		wsHost = "xai-http-bridge"
		wsPath = "/v1/responses"
	} else {
		var err error
		wsURL, err = s.buildOpenAIResponsesWSURL(account)
		if err != nil {
			return fmt.Errorf("build ws url: %w", err)
		}
		if parsedURL, parseErr := url.Parse(wsURL); parseErr == nil && parsedURL != nil {
			wsHost = normalizeOpenAIWSLogValue(parsedURL.Host)
			wsPath = normalizeOpenAIWSLogValue(parsedURL.Path)
		}
	}
	var connectionTarget openAIWSConnectionTarget
	debugEnabled := isOpenAIWSModeDebugEnabled()
	isCodexCLI := openai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator")) || (s.cfg != nil && s.cfg.Gateway.ForceCodexCLI)

	type openAIWSClientPayload struct {
		payloadRaw               []byte
		accountIdentitySourceRaw []byte
		rawForHash               []byte
		promptCacheKey           string
		previousResponseID       string
		originalModel            string
		imageBillingModel        string
		imageSizeTier            string
		imageInputSize           string
		payloadBytes             int
		fingerprintIDs           *codexFingerprintIDs
		requestedReasoningEffort *string
	}
	ingressSessionOriginalModel := ""

	applyPayloadMutation := func(current []byte, path string, value any) ([]byte, error) {
		next, err := sjson.SetBytes(current, path, value)
		if err == nil {
			return next, nil
		}

		// 仅在确实需要修改 payload 且 sjson 失败时，退回 map 路径确保兼容性。
		payload := make(map[string]any)
		if unmarshalErr := json.Unmarshal(current, &payload); unmarshalErr != nil {
			return nil, err
		}
		switch path {
		case "type", "model":
			payload[path] = value
		case "client_metadata." + openAIWSTurnMetadataHeader:
			setOpenAIWSTurnMetadata(payload, fmt.Sprintf("%v", value))
		default:
			return nil, err
		}
		rebuilt, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return nil, marshalErr
		}
		return rebuilt, nil
	}

	parseClientPayload := func(turn int, raw []byte) (openAIWSClientPayload, error) {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "empty websocket request payload", nil)
		}
		if !gjson.ValidBytes(trimmed) {
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", errors.New("invalid json"))
		}

		values := gjson.GetManyBytes(trimmed, "type", "model", "prompt_cache_key", "previous_response_id")
		eventType := strings.TrimSpace(values[0].String())
		normalized := trimmed
		switch eventType {
		case "":
			eventType = "response.create"
			next, setErr := applyPayloadMutation(normalized, "type", eventType)
			if setErr != nil {
				return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", setErr)
			}
			normalized = next
		case "response.create":
		case "response.append":
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(
				coderws.StatusPolicyViolation,
				"response.append is not supported in ws v2; use response.create with previous_response_id",
				nil,
			)
		default:
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(
				coderws.StatusPolicyViolation,
				fmt.Sprintf("unsupported websocket request type: %s", eventType),
				nil,
			)
		}
		requestedReasoningEffort := CanonicalRequestedReasoningEffort(normalized, strings.TrimSpace(values[1].String()))
		if hooks != nil && (hooks.MaxReasoningEffort != "" || len(hooks.ReasoningEffortMappings) > 0) {
			if capped, changed := ApplyOpenAIReasoningEffortPolicy(normalized, hooks.MaxReasoningEffort, hooks.ReasoningEffortMappings); changed {
				normalized = capped
			}
		}
		responsesLite := isOpenAIResponsesLiteWebSocketPayload(normalized)
		if compatibilityBody, compatibilityChanged, compatibilityErr := normalizeOpenAIResponsesWebSocketCompatibilityBody(normalized, account, responsesLite); compatibilityErr != nil {
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", compatibilityErr)
		} else if compatibilityChanged {
			normalized = compatibilityBody
		}
		if account.IsOpenAIOAuthLike() {
			aliasedBody, reverse, aliased, aliasErr := aliasOpenAIOAuthReservedToolNamesBody(normalized)
			if aliasErr != nil {
				return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, aliasErr.Error(), aliasErr)
			}
			updateCodexToolNameReverseForWSFrame(c, normalized, reverse)
			if aliased {
				normalized = aliasedBody
			}
		}

		originalModel := strings.TrimSpace(values[1].String())
		modelMissing := originalModel == ""
		if originalModel == "" {
			// 入站 WS 长会话里，部分客户端只在第一轮 response.create 上声明
			// model，后续 turn 复用同一 session-level model。为避免因省略
			// model 直接断开用户连接，这里回落到上一轮已通过校验的客户端模型，
			// 并在下方写回上游 payload，保证账号模型映射/fast policy/图片权限
			// 仍按同一模型执行。
			originalModel = ingressSessionOriginalModel
			if originalModel == "" {
				return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(
					coderws.StatusPolicyViolation,
					"model is required in response.create payload",
					nil,
				)
			}
		}
		promptCacheKey := strings.TrimSpace(values[2].String())
		previousResponseID := strings.TrimSpace(values[3].String())
		previousResponseIDKind := ClassifyOpenAIPreviousResponseIDKind(previousResponseID)
		if previousResponseID != "" && previousResponseIDKind == OpenAIPreviousResponseIDKindMessageID {
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(
				coderws.StatusPolicyViolation,
				"previous_response_id must be a response.id (resp_*), not a message id",
				nil,
			)
		}
		if turnMetadata := strings.TrimSpace(c.GetHeader(openAIWSTurnMetadataHeader)); turnMetadata != "" {
			next, setErr := applyPayloadMutation(normalized, "client_metadata."+openAIWSTurnMetadataHeader, turnMetadata)
			if setErr != nil {
				return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", setErr)
			}
			normalized = next
		}
		accountIdentitySourceRaw := append([]byte(nil), normalized...)
		accountScopedPayload, accountScoped, scopeErr := applyCodexAccountIdentityClientMetadataRaw(normalized, codexAccountIdentitySource(c, account), getAPIKeyIDFromContext(c))
		if scopeErr != nil {
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket identity metadata", scopeErr)
		}
		if accountScoped {
			normalized = accountScopedPayload
		}
		if responsesLite {
			litePayload, _, liteErr := normalizeOpenAIResponsesLitePayloadForAccount(normalized, account)
			if liteErr != nil {
				return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(
					coderws.StatusPolicyViolation,
					liteErr.Error(),
					liteErr,
				)
			}
			normalized = litePayload
		}
		apiKey := getAPIKeyFromContext(c)
		imageGenerationAllowed := GroupAllowsImageGeneration(apiKeyGroup(apiKey))
		codexImageGenerationExplicitToolPolicy := codexImageGenerationExplicitToolPolicyAllow
		if isCodexCLI {
			codexImageGenerationExplicitToolPolicy = account.CodexImageGenerationExplicitToolPolicy()
		}
		codexBridgeEnabled := isCodexCLI &&
			!isOpenAIResponsesLiteWebSocketPayload(normalized) &&
			imageGenerationAllowed &&
			codexImageGenerationExplicitToolPolicy != codexImageGenerationExplicitToolPolicyStrip &&
			s.isCodexImageGenerationBridgeEnabled(ctx, account, apiKey)
		if codexBridgeEnabled {
			payloadMap := make(map[string]any)
			if err := decodeOpenAIJSONUseNumber(normalized, &payloadMap); err != nil {
				return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", err)
			}
			bridgeModified := false
			if ensureOpenAIResponsesImageGenerationTool(payloadMap) {
				bridgeModified = true
				logOpenAIWSModeInfo("ingress_ws_codex_image_tool_injected account_id=%d", account.ID)
			}
			if ensureOpenAIResponsesImageGenerationToolChoiceAuto(payloadMap) {
				bridgeModified = true
				logOpenAIWSModeInfo("ingress_ws_codex_image_tool_choice_auto account_id=%d", account.ID)
			}
			if normalizeOpenAIResponsesImageGenerationTools(payloadMap) {
				bridgeModified = true
			}
			if applyCodexImageGenerationBridgeInstructions(payloadMap) {
				bridgeModified = true
				logOpenAIWSModeInfo("ingress_ws_codex_image_bridge_instructions_added account_id=%d", account.ID)
			}
			if bridgeModified {
				rebuilt, marshalErr := json.Marshal(payloadMap)
				if marshalErr != nil {
					return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", marshalErr)
				}
				normalized = rebuilt
			}
		}
		requestModel := originalModel
		if hooks != nil && hooks.MapRequestModel != nil {
			mappedModel, mapErr := hooks.MapRequestModel(turn, originalModel)
			if mapErr != nil {
				return openAIWSClientPayload{}, mapErr
			}
			if mappedModel = strings.TrimSpace(mappedModel); mappedModel != "" {
				requestModel = mappedModel
			}
		}
		upstreamModel := normalizeOpenAIModelForUpstream(account, account.GetMappedModel(requestModel))
		if modelMissing || upstreamModel != originalModel {
			next, setErr := applyPayloadMutation(normalized, "model", upstreamModel)
			if setErr != nil {
				return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", setErr)
			}
			normalized = next
		}
		SetOpsUpstreamModel(c, upstreamModel)
		if isCodexCLI && codexImageGenerationExplicitToolPolicy == codexImageGenerationExplicitToolPolicyStrip {
			if stripped, changed, stripErr := stripOpenAIImageGenerationToolsFromRawPayload(normalized); stripErr != nil {
				return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", stripErr)
			} else if changed {
				normalized = stripped
				logOpenAIWSModeInfo("ingress_ws_codex_image_tool_stripped_by_policy account_id=%d", account.ID)
			}
		}
		if stripped, changed, stripErr := stripCodexSparkImageGenerationToolFromRawPayload(normalized, upstreamModel); stripErr != nil {
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", stripErr)
		} else if changed {
			normalized = stripped
			logOpenAIWSModeInfo("ingress_ws_codex_spark_image_tool_stripped account_id=%d", account.ID)
		}
		imageIntent := IsImageGenerationIntentForPlatform(openAIResponsesEndpoint, originalModel, normalized, account.Platform)
		if imageIntent && !imageGenerationAllowed {
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, ImageGenerationPermissionMessage(), nil)
		}
		imageBillingModel := ""
		imageSizeTier := ""
		imageInputSize := ""
		if imageIntent {
			var imageCfgErr error
			imageCfg, imageCfgErr := resolveOpenAIResponsesImageBillingConfigDetailedFromBody(normalized, originalModel)
			if imageCfgErr != nil {
				return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, imageCfgErr.Error(), imageCfgErr)
			}
			imageBillingModel = imageCfg.Model
			imageSizeTier = imageCfg.SizeTier
			imageInputSize = imageCfg.InputSize
		}

		// Apply OpenAI Fast Policy on the response.create frame using the same
		// evaluator/normalize/scope rules as the HTTP entrypoints. This is the
		// single integration point for all WS ingress turns (first + follow-up
		// frames flow through here).
		//
		// Model fallback: first turn still requires model at the handler layer；
		// follow-up response.create frames may omit it and then reuse
		// ingressSessionOriginalModel. We always write a concrete upstream model
		// before evaluating policy, so whitelist / filter behavior remains stable.
		policyApplied, blocked, policyErr := s.applyOpenAIFastPolicyToWSResponseCreate(ctx, account, upstreamModel, normalized)
		if policyErr != nil {
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", policyErr)
		}
		if blocked != nil {
			MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
			// Send a Realtime-style error event to the client first, then
			// signal the handler to close the connection with PolicyViolation.
			// We intentionally do NOT forward this frame upstream.
			//
			// coder/websocket@v1.8.14 Conn.Write is synchronous and flushes
			// the underlying bufio writer before returning (write.go:42 →
			// 307-311), and the subsequent close handshake re-acquires the
			// same writeFrameMu, so the error event is guaranteed to reach
			// the kernel send buffer before any close frame is queued.
			eventBytes := buildOpenAIFastPolicyBlockedWSEvent(blocked)
			if eventBytes != nil {
				writeCtx, cancel := newOpenAIWSDownstreamWriteContext(ctx, hooks, s.openAIWSWriteTimeout())
				_ = clientConn.Write(writeCtx, coderws.MessageText, eventBytes)
				cancel()
			}
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(
				coderws.StatusPolicyViolation,
				blocked.Message,
				blocked,
			)
		}
		normalized = policyApplied
		var fingerprintIDs *codexFingerprintIDs
		if account.IsOpenAIOAuth() && !isOpenAIResponsesCompactPath(c) {
			// rawForHash 保留客户端原值，只有实际出站 payload 使用账号级身份。
			fingerprinted, fpErr := s.applyCodexFingerprintRawForAttempt(ctx, c, account, normalized, true)
			if fpErr != nil {
				return openAIWSClientPayload{}, fmt.Errorf("apply ingress codex fingerprint: %w", fpErr)
			}
			normalized = fingerprinted
			fingerprintIDs = stagedCodexFingerprintIDs(c)
			promptCacheKey = strings.TrimSpace(gjson.GetBytes(normalized, "prompt_cache_key").String())
		}
		lineageNormalized, _, lineageErr := stripOpenAICodexLineageRaw(c, account, normalized)
		if lineageErr != nil {
			return openAIWSClientPayload{}, fmt.Errorf("strip ingress Codex lineage: %w", lineageErr)
		}
		normalized = lineageNormalized
		ingressSessionOriginalModel = originalModel

		return openAIWSClientPayload{
			payloadRaw:               normalized,
			accountIdentitySourceRaw: accountIdentitySourceRaw,
			rawForHash:               trimmed,
			promptCacheKey:           promptCacheKey,
			previousResponseID:       previousResponseID,
			originalModel:            originalModel,
			imageBillingModel:        imageBillingModel,
			imageSizeTier:            imageSizeTier,
			imageInputSize:           imageInputSize,
			payloadBytes:             len(normalized),
			fingerprintIDs:           fingerprintIDs,
			requestedReasoningEffort: requestedReasoningEffort,
		}, nil
	}

	writeClientMessage := func(message []byte) error {
		writeCtx, cancel := newOpenAIWSDownstreamWriteContext(ctx, hooks, s.openAIWSWriteTimeout())
		defer cancel()
		message = restoreCodexToolNamesFromContext(c, message)
		return clientConn.Write(writeCtx, coderws.MessageText, message)
	}

	readClientMessage := func() ([]byte, error) {
		idleTimeout := s.openAIWSIngressInterTurnIdleTimeout()
		msgType, payload, readErr := ReadOpenAIWSClientMessage(
			ctx,
			clientConn,
			idleTimeout,
			coderws.StatusNormalClosure,
			"websocket idle timeout",
		)
		if readErr != nil {
			var closeErr *OpenAIWSClientCloseError
			if errors.As(readErr, &closeErr) && closeErr.StatusCode() == coderws.StatusNormalClosure {
				logOpenAIWSModeInfo("ingress_ws_inter_turn_idle_timeout account_id=%d timeout_seconds=%d", account.ID, int(idleTimeout.Seconds()))
			}
			return nil, readErr
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			return nil, NewOpenAIWSClientCloseError(
				coderws.StatusPolicyViolation,
				fmt.Sprintf("unsupported websocket client message type: %s", msgType.String()),
				nil,
			)
		}
		return payload, nil
	}

	firstPayload, err := parseClientPayload(1, firstClientMessage)
	if err != nil {
		return err
	}

	turnState := strings.TrimSpace(c.GetHeader(openAIWSTurnStateHeader))
	stateStore := s.getOpenAIWSStateStore()
	groupID := getOpenAIGroupIDFromContext(c)
	sessionHash := ""
	preferredConnID := ""
	storeDisabled := false
	refreshIngressRouteState := func(payload openAIWSClientPayload) {
		sessionHash = s.GenerateSessionHash(c, payload.rawForHash)

		preferredConnID = ""
		if stateStore != nil && payload.previousResponseID != "" {
			if connID, ok := getOpenAIWSResponseConn(stateStore, payload.previousResponseID, connectionTarget); ok {
				preferredConnID = connID
			}
		}

		storeDisabled = s.isOpenAIWSStoreDisabledInRequestRaw(payload.payloadRaw, account)
		if stateStore != nil && storeDisabled && payload.previousResponseID == "" && sessionHash != "" {
			if connID, ok := getOpenAIWSSessionConn(stateStore, groupID, sessionHash, connectionTarget); ok {
				preferredConnID = connID
			}
		}
	}
	refreshIngressRouteState(firstPayload)

	if forceHTTPBridge || s.shouldBridgeOpenAIWSHTTP(account, firstPayload.payloadBytes, firstPayload.previousResponseID) {
		logOpenAIWSModeInfo(
			"ingress_ws_http_bridge_start account_id=%d account_type=%s payload_bytes=%d threshold_bytes=%d has_session_hash=%v store_disabled=%v",
			account.ID,
			account.Type,
			firstPayload.payloadBytes,
			s.openAIWSHTTPBridgeThresholdBytes(),
			sessionHash != "",
			storeDisabled,
		)
		currentBridgePayload := firstPayload
		// Keep the first turn as the stable conversation seed. The mapped model
		// is resolved again for each turn below so an in-connection model switch
		// cannot reuse another model's upstream cache identity.
		grokCacheSeedPayload := firstPayload.payloadRaw
		var bridgeReplayInput []json.RawMessage
		bridgeReplayInputExists := false
		var bridgeAccountFailoverInput []json.RawMessage
		bridgeAccountFailoverInputExists := false
		for turn := 1; ; turn++ {
			if turn > 1 && hooks != nil && hooks.BeforeRequest != nil {
				if err := hooks.BeforeRequest(turn, currentBridgePayload.payloadRaw, currentBridgePayload.originalModel); err != nil {
					return err
				}
			}
			if hooks != nil && hooks.BeforeTurn != nil {
				if err := hooks.BeforeTurn(turn); err != nil {
					return err
				}
			}
			if turnState != "" && c != nil && c.Request != nil {
				c.Request.Header.Set(openAIWSTurnStateHeader, turnState)
			}
			bridgePayloadRaw := currentBridgePayload.payloadRaw
			bridgePayloadBytes := currentBridgePayload.payloadBytes
			toolOutputCoverage := AnalyzeToolCallOutputContextCoverageBytes(currentBridgePayload.payloadRaw)
			needsBridgeReplay := currentBridgePayload.previousResponseID != "" ||
				(toolOutputCoverage.HasFunctionCallOutput && !toolOutputCoverage.ContextCoversAllCallIDs)
			turnReplayInput, turnReplayInputExists, replayInputErr := buildOpenAIWSReplayInputSequence(
				bridgeReplayInput,
				bridgeReplayInputExists,
				currentBridgePayload.payloadRaw,
				needsBridgeReplay,
			)
			if replayInputErr != nil {
				return fmt.Errorf("build websocket http bridge replay input: %w", replayInputErr)
			}
			turnAccountFailoverInput, turnAccountFailoverInputExists, failoverInputErr := buildOpenAIWSReplayInputSequence(
				bridgeAccountFailoverInput,
				bridgeAccountFailoverInputExists,
				currentBridgePayload.payloadRaw,
				needsBridgeReplay,
			)
			if failoverInputErr != nil {
				return fmt.Errorf("build websocket account failover input: %w", failoverInputErr)
			}
			if needsBridgeReplay && turnReplayInputExists {
				updatedPayload, setInputErr := setOpenAIWSPayloadInputSequence(
					currentBridgePayload.payloadRaw,
					turnReplayInput,
					true,
				)
				if setInputErr != nil {
					return fmt.Errorf("set websocket http bridge replay input: %w", setInputErr)
				}
				bridgePayloadRaw = updatedPayload
				bridgePayloadBytes = len(updatedPayload)
				logOpenAIWSModeInfo(
					"ingress_ws_http_bridge_replay_input account_id=%d turn=%d input_items=%d previous_response_id_present=%v has_tool_output=%v",
					account.ID,
					turn,
					len(turnReplayInput),
					currentBridgePayload.previousResponseID != "",
					openAIWSRawPayloadHasToolCallOutput(currentBridgePayload.payloadRaw),
				)
			}
			grokCacheIdentity := ""
			if account.Platform == PlatformGrok {
				grokCacheIdentity, err = resolveGrokWSCacheIdentity(
					c,
					account,
					grokCacheSeedPayload,
					currentBridgePayload.payloadRaw,
					currentBridgePayload.originalModel,
				)
				if err != nil {
					return fmt.Errorf("resolve Grok websocket cache identity: %w", err)
				}
			}
			result, bridgeErr := s.proxyOpenAIWSHTTPBridgeTurn(
				ctx,
				c,
				account,
				token,
				bridgePayloadRaw,
				bridgePayloadBytes,
				currentBridgePayload.originalModel,
				currentBridgePayload.imageBillingModel,
				currentBridgePayload.imageSizeTier,
				currentBridgePayload.imageInputSize,
				grokCacheIdentity,
				turn,
				writeClientMessage,
			)
			if bridgeErr == nil && result != nil && result.SucceededForScheduling() {
				s.stageOpenAIUserAffinityResponseAlias(ctx, account.ID, result.RequestID)
			}
			if bridgeErr != nil && isOpenAIWSSessionPreempted(ctx) {
				return errOpenAIWSSessionPreempted
			}
			if hooks != nil && hooks.AfterTurn != nil {
				hooks.AfterTurn(turn, result, bridgeErr)
			}
			if bridgeErr != nil {
				var failoverErr *UpstreamFailoverError
				if turn > 1 && errors.As(bridgeErr, &failoverErr) && failoverErr != nil {
					retryPayload, retrySafe, retryPayloadErr := buildOpenAIWSCurrentTurnRetryPayload(
						currentBridgePayload.accountIdentitySourceRaw,
						turnAccountFailoverInput,
						turnAccountFailoverInputExists,
						currentBridgePayload.originalModel,
					)
					if retryPayloadErr != nil {
						return fmt.Errorf("build websocket current-turn failover payload: %w", retryPayloadErr)
					}
					if !retrySafe {
						retryPayload = nil
					}
					return newOpenAIWSCurrentTurnFailoverError(bridgeErr, retryPayload)
				}
				return bridgeErr
			}
			if result == nil {
				return errors.New("websocket http bridge turn result is nil")
			}
			bridgeReplayInput = cloneOpenAIWSRawMessages(turnReplayInput)
			bridgeReplayInputExists = turnReplayInputExists
			if result.wsReplayInputExists {
				bridgeReplayInput = append(bridgeReplayInput, cloneOpenAIWSRawMessages(result.wsReplayInput)...)
				bridgeReplayInputExists = true
			}
			bridgeAccountFailoverInput = cloneOpenAIWSRawMessages(turnAccountFailoverInput)
			bridgeAccountFailoverInputExists = turnAccountFailoverInputExists
			if len(result.wsAccountFailoverReplayInput) > 0 {
				bridgeAccountFailoverInput = append(
					bridgeAccountFailoverInput,
					cloneOpenAIWSRawMessages(result.wsAccountFailoverReplayInput)...,
				)
				bridgeAccountFailoverInputExists = true
			}
			if bridgeTurnState := strings.TrimSpace(result.ResponseHeaders.Get(openAIWSTurnStateHeader)); bridgeTurnState != "" && openAIForwardResultAllowsTurnStateCommit(result) {
				turnState = bridgeTurnState
				s.bindOpenAITurnStateProvenance(ctx, c, account.ID, sessionHash, bridgeTurnState, s.openAIWSSessionStickyTTL())
			}
			responseID := strings.TrimSpace(result.RequestID)
			if responseID != "" && stateStore != nil {
				ttl := s.openAIWSResponseStickyTTL()
				logOpenAIWSBindResponseAccountWarn(groupID, account.ID, responseID, stateStore.BindResponseAccount(ctx, groupID, responseID, account.ID, ttl))
			}
			nextClientMessage, readErr := readClientMessage()
			if readErr != nil {
				if isOpenAIWSSessionPreempted(ctx) {
					return errOpenAIWSSessionPreempted
				}
				if isOpenAIWSClientDisconnectError(readErr) {
					closeStatus, closeReason := summarizeOpenAIWSReadCloseError(readErr)
					logOpenAIWSModeInfo(
						"ingress_ws_http_bridge_client_closed account_id=%d close_status=%s close_reason=%s",
						account.ID,
						closeStatus,
						truncateOpenAIWSLogValue(closeReason, openAIWSHeaderValueMaxLen),
					)
					return nil
				}
				return fmt.Errorf("read client websocket request: %w", readErr)
			}
			nextPayload, parseErr := parseClientPayload(turn+1, nextClientMessage)
			if parseErr != nil {
				return parseErr
			}
			currentBridgePayload = nextPayload
		}
	}

	firstRoutingFields := gjson.GetManyBytes(firstPayload.payloadRaw, "model", "service_tier")
	wsHeaders, _, buildHdrErr := s.buildOpenAIWSHeaders(
		ctx,
		c,
		account,
		token,
		wsDecision,
		isCodexCLI,
		turnState,
		strings.TrimSpace(c.GetHeader(openAIWSTurnMetadataHeader)),
		firstPayload.promptCacheKey,
		firstRoutingFields[0].String(),
		firstRoutingFields[1].String(),
	)
	if buildHdrErr != nil {
		return fmt.Errorf("build ws headers: %w", buildHdrErr)
	}
	topologyScope := stagedCodexOutboundTopologyScope(c, account)
	connectionTarget = newOpenAIWSConnectionTarget(account, wsDecision.Transport, wsURL, wsHeaders, topologyScope)
	// 首次解析 payload 时握手头尚未构造；现在按实际 fingerprint scope 重新解析首选连接。
	refreshIngressRouteState(firstPayload)
	baseAcquireReq := openAIWSAcquireRequest{
		Account:                 account,
		WSURL:                   wsURL,
		Headers:                 wsHeaders,
		FingerprintSessionScope: stagedCodexFingerprintSessionScopeHash(c),
		TopologyScope:           topologyScope,
		HeadersFactory: func(factoryCtx context.Context, headers http.Header) (http.Header, error) {
			return s.refreshOpenAIAgentIdentityHeaders(factoryCtx, account, headers)
		},
		ProxyURL: func() string {
			if account.ProxyID != nil && account.Proxy != nil {
				return account.Proxy.URL()
			}
			return ""
		}(),
		SessionAffinity: sessionHash,
		ForceNewConn:    false,
	}
	pendingTurnState := ""
	commitPendingTurnState := func() {
		state := strings.TrimSpace(pendingTurnState)
		if state == "" {
			return
		}
		turnState = state
		baseAcquireReq.Headers = cloneOpenAIAttemptHeaderWithTurnState(baseAcquireReq.Headers, state)
		s.bindOpenAITurnStateProvenance(ctx, c, account.ID, sessionHash, state, s.openAIWSSessionStickyTTL())
		pendingTurnState = ""
	}
	pool := s.getOpenAIWSConnPool()
	if pool == nil {
		return errors.New("openai ws conn pool is nil")
	}

	logOpenAIWSModeInfo(
		"ingress_ws_protocol_confirm account_id=%d account_type=%s transport=%s ws_host=%s ws_path=%s ws_mode=%s store_disabled=%v has_session_hash=%v has_previous_response_id=%v",
		account.ID,
		account.Type,
		normalizeOpenAIWSLogValue(string(wsDecision.Transport)),
		wsHost,
		wsPath,
		normalizeOpenAIWSLogValue(ingressMode),
		storeDisabled,
		sessionHash != "",
		firstPayload.previousResponseID != "",
	)

	if debugEnabled {
		logOpenAIWSModeDebug(
			"ingress_ws_start account_id=%d account_type=%s transport=%s ws_host=%s preferred_conn_id=%s has_session_hash=%v has_previous_response_id=%v store_disabled=%v",
			account.ID,
			account.Type,
			normalizeOpenAIWSLogValue(string(wsDecision.Transport)),
			wsHost,
			truncateOpenAIWSLogValue(preferredConnID, openAIWSIDValueMaxLen),
			sessionHash != "",
			firstPayload.previousResponseID != "",
			storeDisabled,
		)
	}
	if firstPayload.previousResponseID != "" {
		firstPreviousResponseIDKind := ClassifyOpenAIPreviousResponseIDKind(firstPayload.previousResponseID)
		logOpenAIWSModeInfo(
			"ingress_ws_continuation_probe account_id=%d turn=%d previous_response_id=%s previous_response_id_kind=%s preferred_conn_id=%s session_hash=%s header_session_id=%s header_conversation_id=%s has_turn_state=%v turn_state_len=%d has_prompt_cache_key=%v store_disabled=%v",
			account.ID,
			1,
			truncateOpenAIWSLogValue(firstPayload.previousResponseID, openAIWSIDValueMaxLen),
			normalizeOpenAIWSLogValue(firstPreviousResponseIDKind),
			truncateOpenAIWSLogValue(preferredConnID, openAIWSIDValueMaxLen),
			truncateOpenAIWSLogValue(sessionHash, 12),
			openAIWSHeaderValueForLog(baseAcquireReq.Headers, "session_id"),
			openAIWSHeaderValueForLog(baseAcquireReq.Headers, "conversation_id"),
			turnState != "",
			len(turnState),
			firstPayload.promptCacheKey != "",
			storeDisabled,
		)
	}

	acquireTimeout := s.openAIWSAcquireTimeout()
	if acquireTimeout <= 0 {
		acquireTimeout = 30 * time.Second
	}

	agentTaskRecoveryTried := false
	var acquireTurnLease func(int, string, bool, bool) (*openAIWSConnLease, error)
	acquireTurnLease = func(turn int, preferred string, forcePreferredConn, forceNewConn bool) (*openAIWSConnLease, error) {
		// 上一次未被有效输出确认的握手状态不能进入下一次连接尝试。
		pendingTurnState = ""
		req := cloneOpenAIWSAcquireRequest(baseAcquireReq)
		req.PreferredConnID = strings.TrimSpace(preferred)
		req.ForcePreferredConn = forcePreferredConn
		// dedicated 模式下每次获取均新建连接，避免跨会话复用残留上下文。
		req.ForceNewConn = dedicatedMode || forceNewConn
		acquireCtx, acquireCancel := context.WithTimeout(ctx, acquireTimeout)
		lease, acquireErr := pool.Acquire(acquireCtx, req)
		acquireCancel()
		var dialErr *openAIWSDialError
		if acquireErr != nil && s.isAgentIdentityAccount(ctx, account) && errors.As(acquireErr, &dialErr) && isAgentIdentityTaskInvalidWSDialError(dialErr) && !agentTaskRecoveryTried {
			agentTaskRecoveryTried = true
			if recoveryErr := s.recoverAgentIdentityTask(ctx, account, account.GetCredential("task_id")); recoveryErr != nil {
				return nil, fmt.Errorf("agent identity task recovery failed: %w", recoveryErr)
			}
			return acquireTurnLease(turn, preferred, forcePreferredConn, forceNewConn)
		}
		if acquireErr != nil {
			if isOpenAIWSSessionPreempted(ctx) {
				return nil, errOpenAIWSSessionPreempted
			}
			canonicalModel := canonicalOpenAIAccountSchedulingModel(account, ingressSessionOriginalModel)
			s.handleOpenAIWSDialTransientFailure(ctx, account, canonicalModel, acquireErr)
			dialStatus, dialClass, dialCloseStatus, dialCloseReason, dialRespServer, dialRespVia, dialRespCFRay, dialRespReqID := summarizeOpenAIWSDialError(acquireErr)
			logOpenAIWSModeInfo(
				"ingress_ws_upstream_acquire_fail account_id=%d turn=%d reason=%s dial_status=%d dial_class=%s dial_close_status=%s dial_close_reason=%s dial_resp_server=%s dial_resp_via=%s dial_resp_cf_ray=%s dial_resp_x_request_id=%s cause=%s preferred_conn_id=%s force_preferred_conn=%v ws_host=%s ws_path=%s proxy_enabled=%v",
				account.ID,
				turn,
				normalizeOpenAIWSLogValue(classifyOpenAIWSAcquireError(acquireErr)),
				dialStatus,
				dialClass,
				dialCloseStatus,
				truncateOpenAIWSLogValue(dialCloseReason, openAIWSHeaderValueMaxLen),
				dialRespServer,
				dialRespVia,
				dialRespCFRay,
				dialRespReqID,
				truncateOpenAIWSLogValue(acquireErr.Error(), openAIWSLogValueMaxLen),
				truncateOpenAIWSLogValue(preferred, openAIWSIDValueMaxLen),
				forcePreferredConn,
				wsHost,
				wsPath,
				account.ProxyID != nil && account.Proxy != nil,
			)
			var dialErr *openAIWSDialError
			if errors.As(acquireErr, &dialErr) && dialErr != nil && dialErr.StatusCode == http.StatusTooManyRequests {
				s.persistOpenAIWSRateLimitSignal(ctx, account, dialErr.ResponseHeaders, nil, "rate_limit_exceeded", "rate_limit_error", strings.TrimSpace(acquireErr.Error()))
				return nil, s.newOpenAIWSRateLimitFailoverError(account, dialErr.ResponseHeaders, nil, acquireErr.Error())
			}
			if errors.Is(acquireErr, errOpenAIWSPreferredConnUnavailable) {
				return nil, NewOpenAIWSClientCloseError(
					coderws.StatusPolicyViolation,
					"upstream continuation connection is unavailable; please restart the conversation",
					acquireErr,
				)
			}
			if errors.Is(acquireErr, context.DeadlineExceeded) || errors.Is(acquireErr, errOpenAIWSConnQueueFull) {
				return nil, NewOpenAIWSClientCloseError(
					coderws.StatusTryAgainLater,
					"upstream websocket is busy, please retry later",
					acquireErr,
				)
			}
			return nil, acquireErr
		}
		connID := strings.TrimSpace(lease.ConnID())
		if handshakeTurnState := strings.TrimSpace(lease.HandshakeHeader(openAIWSTurnStateHeader)); handshakeTurnState != "" {
			pendingTurnState = handshakeTurnState
		}
		logOpenAIWSModeInfo(
			"ingress_ws_upstream_connected account_id=%d turn=%d conn_id=%s conn_reused=%v conn_pick_ms=%d queue_wait_ms=%d preferred_conn_id=%s",
			account.ID,
			turn,
			truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
			lease.Reused(),
			lease.ConnPickDuration().Milliseconds(),
			lease.QueueWaitDuration().Milliseconds(),
			truncateOpenAIWSLogValue(preferred, openAIWSIDValueMaxLen),
		)
		return lease, nil
	}

	var rejectedFieldRetryState *openAIResponsesRejectedFieldRetryState
	sendAndRelay := func(turn int, lease *openAIWSConnLease, payload []byte, payloadBytes int, originalModel string, imageBillingModel string, imageSizeTier string, imageInputSize string, fingerprintIDs *codexFingerprintIDs, requestedReasoningEffort *string) (*OpenAIForwardResult, error) {
		responseModelObserver := &upstreamResponseModelObserver{}
		if lease == nil {
			return nil, errors.New("upstream websocket lease is nil")
		}
		releaseSubagentSlot, gateErr := s.acquireCodexSubagentSlot(ctx, account, fingerprintIDs)
		if gateErr != nil {
			return nil, gateErr
		}
		defer releaseSubagentSlot()
		turnStart := time.Now()
		wroteDownstream := false
		sawUpstreamEvent := false
		if err := lease.WriteJSONWithContextTimeout(ctx, json.RawMessage(payload), s.openAIWSWriteTimeout()); err != nil {
			return nil, wrapOpenAIWSIngressTurnError(
				"write_upstream",
				fmt.Errorf("write upstream websocket request: %w", err),
				false,
			)
		}
		if debugEnabled {
			logOpenAIWSModeDebug(
				"ingress_ws_turn_request_sent account_id=%d turn=%d conn_id=%s payload_bytes=%d",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(lease.ConnID(), openAIWSIDValueMaxLen),
				payloadBytes,
			)
		}

		responseID := ""
		usage := OpenAIUsage{}
		imageCounter := newOpenAIImageOutputCounter()
		var firstTokenMs *int
		reqStream := openAIWSPayloadBoolFromRaw(payload, "stream", true)
		turnPreviousResponseID := openAIWSPayloadStringFromRaw(payload, "previous_response_id")
		turnPreviousResponseIDKind := ClassifyOpenAIPreviousResponseIDKind(turnPreviousResponseID)
		turnPromptCacheKey := openAIWSPayloadStringFromRaw(payload, "prompt_cache_key")
		turnHasFunctionCallOutput := openAIWSRawPayloadHasToolCallOutput(payload)
		turnStoreDisabled := s.isOpenAIWSStoreDisabledInRequestRaw(payload, account)
		eventCount := 0
		tokenEventCount := 0
		terminalEventCount := 0
		turnAccepted := false
		replayCollector := &openAIWSResponseReplayCollector{}
		firstEventType := ""
		lastEventType := ""
		needModelReplace := false
		clientDisconnected := false
		mappedModel := ""
		var mappedModelBytes []byte
		if originalModel != "" {
			mappedModel = strings.TrimSpace(gjson.GetBytes(payload, "model").String())
			if mappedModel == "" {
				mappedModel = normalizeOpenAIModelForUpstream(account, account.GetMappedModel(originalModel))
			}
			needModelReplace = mappedModel != "" && mappedModel != originalModel
			if needModelReplace {
				mappedModelBytes = []byte(mappedModel)
			}
		}
		for {
			upstreamMessage, readErr := lease.ReadMessageWithContextTimeout(ctx, s.openAIWSReadTimeout())
			if readErr != nil {
				lease.MarkBroken()
				return nil, wrapOpenAIWSIngressTurnErrorAfterEvent(
					"read_upstream",
					fmt.Errorf("read upstream websocket event: %w", readErr),
					wroteDownstream,
					sawUpstreamEvent,
				)
			}
			sawUpstreamEvent = true
			if normalized, changed := normalizeCompletedImageGenerationStatus(upstreamMessage); changed {
				upstreamMessage = normalized
			}

			eventType, eventResponseID, _ := parseOpenAIWSEventEnvelope(upstreamMessage)
			if isOpenAIWSTurnStateCommitEvent(eventType) {
				commitPendingTurnState()
			}
			if !turnAccepted && isOpenAIWSTurnAcceptedEvent(eventType) {
				turnAccepted = true
				if hooks != nil && hooks.OnTurnAccepted != nil {
					hooks.OnTurnAccepted(turn)
				}
			}
			responseModelObserver.ObserveOpenAI(upstreamMessage, eventType)
			if responseID == "" && eventResponseID != "" {
				responseID = eventResponseID
			}
			if eventType != "" {
				eventCount++
				if firstEventType == "" {
					firstEventType = eventType
				}
				lastEventType = eventType
			}
			if eventType == "error" {
				s.handleOpenAIWSErrorEventTransientFailure(ctx, account, mappedModel, lease.HandshakeHeaders(), upstreamMessage)
				errCodeRaw, errTypeRaw, errMsgRaw := parseOpenAIWSErrorEventFields(upstreamMessage)
				statusCode := openAIWSRejectedFieldRetryHTTPStatus(upstreamMessage)
				if !wroteDownstream && statusCode == http.StatusBadRequest && rejectedFieldRetryState != nil {
					retryBody, retryReason, changed, retryErr := normalizeOpenAIResponsesRejectedFieldRetryBody(
						statusCode,
						payload,
						upstreamMessage,
					)
					if retryErr != nil {
						return nil, fmt.Errorf("normalize websocket rejected field retry: %w", retryErr)
					}
					if changed && rejectedFieldRetryState.Allow(retryBody) {
						logOpenAIWSModeInfo(
							"ingress_ws_rejected_field_retry account_id=%d turn=%d conn_id=%s reason=%s",
							account.ID,
							turn,
							truncateOpenAIWSLogValue(lease.ConnID(), openAIWSIDValueMaxLen),
							truncateOpenAIWSLogValue(retryReason, openAIWSLogValueMaxLen),
						)
						return nil, &openAIWSRejectedFieldRetryError{
							body:   append([]byte(nil), retryBody...),
							reason: retryReason,
						}
					}
				}
				s.persistOpenAIWSRateLimitSignal(ctx, account, lease.HandshakeHeaders(), upstreamMessage, errCodeRaw, errTypeRaw, errMsgRaw)
				fallbackReason, _ := classifyOpenAIWSErrorEventFromRaw(errCodeRaw, errTypeRaw, errMsgRaw)
				errCode, errType, errMessage := summarizeOpenAIWSErrorEventFieldsFromRaw(errCodeRaw, errTypeRaw, errMsgRaw)
				logOpenAIWSModeInfo(
					"ingress_ws_error_event account_id=%d turn=%d conn_id=%s idx=%d fallback_reason=%s err_code=%s err_type=%s err_message=%s previous_response_id=%s previous_response_id_kind=%s response_id=%s store_disabled=%v has_prompt_cache_key=%v",
					account.ID,
					turn,
					truncateOpenAIWSLogValue(lease.ConnID(), openAIWSIDValueMaxLen),
					eventCount,
					truncateOpenAIWSLogValue(fallbackReason, openAIWSLogValueMaxLen),
					errCode,
					errType,
					errMessage,
					truncateOpenAIWSLogValue(turnPreviousResponseID, openAIWSIDValueMaxLen),
					normalizeOpenAIWSLogValue(turnPreviousResponseIDKind),
					truncateOpenAIWSLogValue(responseID, openAIWSIDValueMaxLen),
					turnStoreDisabled,
					turnPromptCacheKey != "",
				)
				recoverablePrevNotFound := fallbackReason == openAIWSIngressStagePreviousResponseNotFound &&
					turnPreviousResponseID != "" &&
					!turnHasFunctionCallOutput &&
					s.openAIWSIngressPreviousResponseRecoveryEnabled() &&
					!wroteDownstream
				if recoverablePrevNotFound {
					// 可恢复场景使用非 error 关键字日志，避免被 LegacyPrintf 误判为 ERROR 级别。
					logOpenAIWSModeInfo(
						"ingress_ws_prev_response_recoverable account_id=%d turn=%d conn_id=%s idx=%d reason=%s code=%s type=%s message=%s previous_response_id=%s previous_response_id_kind=%s response_id=%s store_disabled=%v has_prompt_cache_key=%v",
						account.ID, turn, truncateOpenAIWSLogValue(lease.ConnID(), openAIWSIDValueMaxLen), eventCount,
						truncateOpenAIWSLogValue(fallbackReason, openAIWSLogValueMaxLen), errCode, errType, errMessage,
						truncateOpenAIWSLogValue(turnPreviousResponseID, openAIWSIDValueMaxLen),
						normalizeOpenAIWSLogValue(turnPreviousResponseIDKind), truncateOpenAIWSLogValue(responseID, openAIWSIDValueMaxLen),
						turnStoreDisabled, turnPromptCacheKey != "",
					)
				} else {
					logOpenAIWSModeInfo(
						"ingress_ws_error_event account_id=%d turn=%d conn_id=%s idx=%d fallback_reason=%s err_code=%s err_type=%s err_message=%s previous_response_id=%s previous_response_id_kind=%s response_id=%s store_disabled=%v has_prompt_cache_key=%v",
						account.ID, turn, truncateOpenAIWSLogValue(lease.ConnID(), openAIWSIDValueMaxLen), eventCount,
						truncateOpenAIWSLogValue(fallbackReason, openAIWSLogValueMaxLen), errCode, errType, errMessage,
						truncateOpenAIWSLogValue(turnPreviousResponseID, openAIWSIDValueMaxLen),
						normalizeOpenAIWSLogValue(turnPreviousResponseIDKind), truncateOpenAIWSLogValue(responseID, openAIWSIDValueMaxLen),
						turnStoreDisabled, turnPromptCacheKey != "",
					)
				}
				// previous_response_not_found 仅在无 tool output 且尚未向客户端输出时恢复一次。
				if recoverablePrevNotFound {
					lease.MarkBroken()
					errMsg := strings.TrimSpace(errMsgRaw)
					if errMsg == "" {
						errMsg = "previous response not found"
					}
					return nil, wrapOpenAIWSIngressTurnError(openAIWSIngressStagePreviousResponseNotFound, errors.New(errMsg), false)
				}
				if !wroteDownstream && isOpenAIWSRateLimitError(errCodeRaw, errTypeRaw, errMsgRaw) {
					lease.MarkBroken()
					return nil, s.newOpenAIWSRateLimitFailoverError(account, lease.HandshakeHeaders(), upstreamMessage, errMsgRaw)
				}
			}
			isTokenEvent := isOpenAIWSTokenEvent(eventType)
			if isTokenEvent {
				tokenEventCount++
			}
			// error 是已观察到的上游业务终态：原样转发后结束本轮，禁止按传输故障重放。
			isTerminalEvent := eventType == "error" || isOpenAIWSTerminalEvent(eventType)
			if isTerminalEvent {
				terminalEventCount++
			}
			if firstTokenMs == nil && isTokenEvent {
				ms := int(time.Since(turnStart).Milliseconds())
				firstTokenMs = &ms
			}
			if openAIWSMessageShouldParseUsage(eventType, upstreamMessage) {
				parseOpenAIWSResponseUsageFromCompletedEvent(upstreamMessage, &usage)
			}
			imageCounter.AddSSEData(upstreamMessage)

			if eventType == "response.failed" {
				if hit, code, msg := detectOpenAICyberPolicy(upstreamMessage); hit {
					MarkOpsCyberPolicy(c, CyberPolicyMark{
						Code:           code,
						Message:        msg,
						Body:           truncateString(string(upstreamMessage), 4096),
						UpstreamStatus: http.StatusOK,
						UpstreamInTok:  usage.InputTokens,
						UpstreamOutTok: usage.OutputTokens,
					})
				}
			}
			if !clientDisconnected {
				if needModelReplace && len(mappedModelBytes) > 0 && openAIWSEventMayContainModel(eventType) && bytes.Contains(upstreamMessage, mappedModelBytes) {
					upstreamMessage = replaceOpenAIWSMessageModel(upstreamMessage, mappedModel, originalModel)
				}
				if openAIWSEventMayContainToolCalls(eventType) && openAIWSMessageLikelyContainsToolCalls(upstreamMessage) {
					if corrected, changed := s.toolCorrector.CorrectToolCallsInSSEBytes(upstreamMessage); changed {
						upstreamMessage = corrected
					}
				}
				replayCollector.AddEvent(eventType, upstreamMessage)
				if err := writeClientMessage(upstreamMessage); err != nil {
					if isOpenAIWSClientDisconnectError(err) {
						clientDisconnected = true
						closeStatus, closeReason := summarizeOpenAIWSReadCloseError(err)
						logOpenAIWSModeInfo(
							"ingress_ws_client_disconnected_drain account_id=%d turn=%d conn_id=%s close_status=%s close_reason=%s",
							account.ID,
							turn,
							truncateOpenAIWSLogValue(lease.ConnID(), openAIWSIDValueMaxLen),
							closeStatus,
							truncateOpenAIWSLogValue(closeReason, openAIWSHeaderValueMaxLen),
						)
					} else {
						return nil, wrapOpenAIWSIngressTurnErrorAfterEvent(
							"write_client",
							fmt.Errorf("write client websocket event: %w", err),
							wroteDownstream,
							sawUpstreamEvent,
						)
					}
				} else {
					wroteDownstream = true
					markOpenAIWSClientVisibleFailure(c, eventType, upstreamMessage)
				}
			}
			if isTerminalEvent {
				terminalEvent := s.handleOpenAIWSTerminalTransientFailure(ctx, account, mappedModel, lease.HandshakeHeaders(), upstreamMessage)
				// 客户端已断连时，上游连接的 session 状态不可信，标记 broken 避免回池复用。
				if clientDisconnected {
					lease.MarkBroken()
				}
				firstTokenMsValue := -1
				if firstTokenMs != nil {
					firstTokenMsValue = *firstTokenMs
				}
				if debugEnabled {
					logOpenAIWSModeDebug(
						"ingress_ws_turn_completed account_id=%d turn=%d conn_id=%s response_id=%s duration_ms=%d events=%d token_events=%d terminal_events=%d first_event=%s last_event=%s first_token_ms=%d client_disconnected=%v",
						account.ID,
						turn,
						truncateOpenAIWSLogValue(lease.ConnID(), openAIWSIDValueMaxLen),
						truncateOpenAIWSLogValue(responseID, openAIWSIDValueMaxLen),
						time.Since(turnStart).Milliseconds(),
						eventCount,
						tokenEventCount,
						terminalEventCount,
						truncateOpenAIWSLogValue(firstEventType, openAIWSLogValueMaxLen),
						truncateOpenAIWSLogValue(lastEventType, openAIWSLogValueMaxLen),
						firstTokenMsValue,
						clientDisconnected,
					)
				}
				imageCount := imageCounter.Count()
				result := &OpenAIForwardResult{
					RequestID:                     responseID,
					Usage:                         usage,
					Model:                         originalModel,
					UpstreamModel:                 mappedModel,
					UpstreamResponseModel:         responseModelObserver.Model(),
					UpstreamResponseModelConflict: responseModelObserver.Conflict(),
					UpstreamResponseServiceTier:   responseModelObserver.ServiceTier(),
					ServiceTier:                   resolvedOpenAIUpstreamServiceTierFromObserver(responseModelObserver, extractOpenAIServiceTierFromBody(payload)),
					ReasoningEffort:               ApplyThinkingEnabledFallback(extractOpenAIReasoningEffortFromBody(payload, mappedModel, originalModel), payload, mappedModel),
					RequestedReasoningEffort:      requestedReasoningEffort,
					Stream:                        reqStream,
					OpenAIWSMode:                  true,
					UpstreamTerminalEvent:         terminalEvent,
					ResponseHeaders:               lease.HandshakeHeaders(),
					Duration:                      time.Since(turnStart),
					FirstTokenMs:                  firstTokenMs,
				}
				if replayInput := replayCollector.Items(); len(replayInput) > 0 {
					result.wsReplayInput = replayInput
					result.wsReplayInputExists = true
				}
				result.wsReplayComplete, result.wsReplayReason = replayCollector.Replayable()
				if imageCount > 0 {
					result.ImageCount = imageCount
					result.ImageSize = imageSizeTier
					result.ImageInputSize = imageInputSize
					result.ImageOutputSizes = imageCounter.Sizes()
					result.BillingModel = imageBillingModel
				}
				return result, nil
			}
		}
	}

	currentPayload := firstPayload.payloadRaw
	currentFingerprintIDs := firstPayload.fingerprintIDs
	currentOriginalModel := firstPayload.originalModel
	currentImageBillingModel := firstPayload.imageBillingModel
	currentImageSizeTier := firstPayload.imageSizeTier
	currentImageInputSize := firstPayload.imageInputSize
	currentPayloadBytes := firstPayload.payloadBytes
	currentRequestedReasoningEffort := firstPayload.requestedReasoningEffort
	isStrictAffinityTurn := func(payload []byte) bool {
		if !storeDisabled {
			return false
		}
		return strings.TrimSpace(openAIWSPayloadStringFromRaw(payload, "previous_response_id")) != ""
	}
	var sessionLease *openAIWSConnLease
	sessionConnID := ""
	pinnedSessionConnID := ""
	unpinSessionConn := func(connID string) {
		connID = strings.TrimSpace(connID)
		if connID == "" || pinnedSessionConnID != connID {
			return
		}
		pool.UnpinConn(account.ID, connID)
		pinnedSessionConnID = ""
	}
	pinSessionConn := func(connID string) {
		if !storeDisabled {
			return
		}
		connID = strings.TrimSpace(connID)
		if connID == "" || pinnedSessionConnID == connID {
			return
		}
		if pinnedSessionConnID != "" {
			pool.UnpinConn(account.ID, pinnedSessionConnID)
			pinnedSessionConnID = ""
		}
		if pool.PinConn(account.ID, connID) {
			pinnedSessionConnID = connID
		}
	}
	// lastTurnClean 标记最后一轮是否收到终端事件；传输异常或客户端断连时连接不可回池。
	lastTurnClean := false
	releaseSessionLease := func() {
		if sessionLease == nil {
			return
		}
		if !lastTurnClean {
			sessionLease.MarkBroken()
		}
		unpinSessionConn(sessionConnID)
		sessionLease.Release()
		if debugEnabled {
			logOpenAIWSModeDebug(
				"ingress_ws_upstream_released account_id=%d conn_id=%s",
				account.ID,
				truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
			)
		}
	}
	defer releaseSessionLease()

	turn := 1
	rejectedFieldRetryState = newOpenAIResponsesRejectedFieldRetryState(currentPayload)
	turnRetry := 0
	turnPrevRecoveryTried := false
	lastTurnResponseID := ""
	lastTurnPayload := []byte(nil)
	var lastTurnStrictState *openAIWSIngressPreviousTurnStrictState
	lastTurnReplayInput := []json.RawMessage(nil)
	lastTurnReplayInputExists := false
	lastTurnReplayComplete := false
	lastTurnReplayReason := "missing_parent_checkpoint"
	currentTurnReplayInput := []json.RawMessage(nil)
	currentTurnReplayInputExists := false
	currentTurnReplayComplete := false
	currentTurnReplayReason := ""
	skipBeforeTurn := false
	hasCurrentOrReplayFunctionCallOutput := func(payload []byte) bool {
		if openAIWSRawPayloadHasToolCallOutput(payload) {
			return true
		}
		return currentTurnReplayInputExists && openAIWSRawItemsHasFunctionCallOutput(currentTurnReplayInput)
	}
	resetSessionLease := func(markBroken bool) {
		if sessionLease == nil {
			return
		}
		if markBroken {
			sessionLease.MarkBroken()
		}
		releaseSessionLease()
		sessionLease = nil
		sessionConnID = ""
		preferredConnID = ""
	}
	cleanupStaleConnBindings := func(connID string) {
		connID = strings.TrimSpace(connID)
		if stateStore == nil || connID == "" {
			return
		}
		deleted := deleteOpenAIWSConnBindings(stateStore, connID)
		if deleted == 0 {
			for _, responseID := range []string{
				openAIWSPayloadStringFromRaw(currentPayload, "previous_response_id"),
				lastTurnResponseID,
			} {
				if boundConnID, ok := stateStore.GetResponseConn(responseID); ok && strings.TrimSpace(boundConnID) == connID {
					stateStore.DeleteResponseConn(responseID)
					deleted++
				}
			}
			if boundConnID, ok := stateStore.GetSessionConn(groupID, sessionHash); ok && strings.TrimSpace(boundConnID) == connID {
				stateStore.DeleteSessionConn(groupID, sessionHash)
				deleted++
			}
		}
		s.recordOpenAIWSIngressStaleBindingCleanup(deleted)
	}
	recoverIngressPrevResponseNotFound := func(relayErr error, turn int, connID string) bool {
		if !isOpenAIWSIngressPreviousResponseNotFound(relayErr) || turnPrevRecoveryTried || !s.openAIWSIngressPreviousResponseRecoveryEnabled() {
			return false
		}
		// 携带 function_call_output 时必须保留续链锚点，避免工具结果失去所属上下文。
		if hasCurrentOrReplayFunctionCallOutput(currentPayload) {
			return false
		}
		turnPrevRecoveryTried = true
		updatedPayload, removed, dropErr := dropPreviousResponseIDFromRawPayload(currentPayload)
		if dropErr != nil || !removed {
			return false
		}
		updatedWithInput, setInputErr := setOpenAIWSPayloadInputSequence(updatedPayload, currentTurnReplayInput, currentTurnReplayInputExists)
		if setInputErr != nil {
			return false
		}
		logOpenAIWSModeInfo(
			"ingress_ws_prev_response_recovery account_id=%d turn=%d conn_id=%s action=drop_previous_response_id retry=1",
			account.ID,
			turn,
			truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
		)
		currentPayload = updatedWithInput
		resetSessionLease(true)
		skipBeforeTurn = true
		return true
	}
	retryIngressTurn := func(relayErr error, turn int, connID string) bool {
		if !isOpenAIWSIngressTurnRetryable(relayErr) || turnRetry >= 1 {
			return false
		}
		if isStrictAffinityTurn(currentPayload) {
			return false
		}
		turnRetry++
		logOpenAIWSModeInfo(
			"ingress_ws_turn_retry account_id=%d turn=%d retry=%d reason=%s conn_id=%s",
			account.ID,
			turn,
			turnRetry,
			truncateOpenAIWSLogValue(openAIWSIngressTurnRetryReason(relayErr), openAIWSLogValueMaxLen),
			truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
		)
		resetSessionLease(true)
		skipBeforeTurn = true
		return true
	}
	for {
		if turn > 1 && !skipBeforeTurn && hooks != nil && hooks.BeforeRequest != nil {
			if err := hooks.BeforeRequest(turn, currentPayload, currentOriginalModel); err != nil {
				return err
			}
		}
		if !skipBeforeTurn && hooks != nil && hooks.BeforeTurn != nil {
			if err := hooks.BeforeTurn(turn); err != nil {
				return err
			}
		}
		skipBeforeTurn = false
		currentPreviousResponseID := openAIWSPayloadStringFromRaw(currentPayload, "previous_response_id")
		expectedPrev := strings.TrimSpace(lastTurnResponseID)
		toolSignals := ToolContinuationSignals{
			HasFunctionCallOutput: openAIWSRawPayloadHasToolCallOutput(currentPayload),
		}
		if toolSignals.HasFunctionCallOutput {
			var currentReqBody map[string]any
			if err := json.Unmarshal(currentPayload, &currentReqBody); err == nil {
				toolSignals = AnalyzeToolContinuationSignals(currentReqBody)
			}
		}
		hasFunctionCallOutput := toolSignals.HasFunctionCallOutput
		// store=false + function_call_output 场景必须有续链锚点。
		// 若客户端未传 previous_response_id，优先回填上一轮响应 ID，避免上游报 call_id 无法关联。
		if shouldInferIngressFunctionCallOutputPreviousResponseID(
			storeDisabled,
			turn,
			toolSignals,
			currentPreviousResponseID,
			expectedPrev,
		) {
			updatedPayload, setPrevErr := setPreviousResponseIDToRawPayload(currentPayload, expectedPrev)
			if setPrevErr != nil {
				logOpenAIWSModeInfo(
					"ingress_ws_function_call_output_prev_infer_skip account_id=%d turn=%d conn_id=%s reason=set_previous_response_id_error cause=%s expected_previous_response_id=%s",
					account.ID,
					turn,
					truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(setPrevErr.Error(), openAIWSLogValueMaxLen),
					truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
				)
			} else {
				currentPayload = updatedPayload
				currentPreviousResponseID = expectedPrev
				logOpenAIWSModeInfo(
					"ingress_ws_function_call_output_prev_infer account_id=%d turn=%d conn_id=%s action=set_previous_response_id previous_response_id=%s",
					account.ID,
					turn,
					truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
				)
			}
		}
		parentReplayInput := lastTurnReplayInput
		parentReplayInputExists := lastTurnReplayInputExists
		parentReplayComplete := lastTurnReplayComplete
		parentReplayReason := lastTurnReplayReason
		previousResponseSourceConnID := ""
		usesLastTurnCheckpoint := currentPreviousResponseID != "" && expectedPrev != "" && currentPreviousResponseID == expectedPrev
		if currentPreviousResponseID != "" {
			if stateStore != nil {
				previousResponseSourceConnID, _ = getOpenAIWSResponseConn(stateStore, currentPreviousResponseID, connectionTarget)
			}
			if usesLastTurnCheckpoint {
				if previousResponseSourceConnID == "" {
					previousResponseSourceConnID = sessionConnID
				}
			} else {
				parentReplayInput = nil
				parentReplayInputExists = false
				parentReplayComplete = false
				parentReplayReason = "missing_parent_checkpoint"
				if checkpoint, found := getOpenAIWSReplayCheckpoint(stateStore, groupID, currentPreviousResponseID, connectionTarget); found {
					parentReplayInput = checkpoint.FullInput
					parentReplayInputExists = checkpoint.FullInputExists
					parentReplayComplete = checkpoint.Replayable
					parentReplayReason = checkpoint.UnavailableReason
					if previousResponseSourceConnID == "" {
						previousResponseSourceConnID = checkpoint.SourceConnID
					}
				}
			}
		} else {
			parentReplayInput = nil
			parentReplayInputExists = false
			parentReplayComplete = true
			parentReplayReason = ""
		}
		nextReplayInput, nextReplayInputExists, replayInputErr := buildOpenAIWSReplayInputSequence(
			parentReplayInput,
			parentReplayInputExists,
			currentPayload,
			currentPreviousResponseID != "",
		)
		if replayInputErr != nil {
			logOpenAIWSModeInfo(
				"ingress_ws_replay_input_skip account_id=%d turn=%d conn_id=%s reason=build_error cause=%s",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(replayInputErr.Error(), openAIWSLogValueMaxLen),
			)
			currentTurnReplayInput = nil
			currentTurnReplayInputExists = false
			currentTurnReplayComplete = false
			currentTurnReplayReason = "build_replay_input_error"
		} else {
			currentTurnReplayInput = nextReplayInput
			currentTurnReplayInputExists = nextReplayInputExists
			currentTurnReplayComplete = currentPreviousResponseID == "" || parentReplayComplete
			currentTurnReplayReason = parentReplayReason
		}
		replayHasFunctionCallOutput := currentTurnReplayInputExists &&
			openAIWSRawItemsHasFunctionCallOutput(currentTurnReplayInput)
		hasFunctionCallOutput = hasFunctionCallOutput || replayHasFunctionCallOutput
		if storeDisabled && turn > 1 && currentPreviousResponseID != "" {
			shouldKeepPreviousResponseID := false
			strictReason := ""
			var strictErr error
			if lastTurnStrictState != nil {
				shouldKeepPreviousResponseID, strictReason, strictErr = shouldKeepIngressPreviousResponseIDWithStrictState(
					lastTurnStrictState,
					currentPayload,
					lastTurnResponseID,
					hasFunctionCallOutput,
				)
			} else {
				shouldKeepPreviousResponseID, strictReason, strictErr = shouldKeepIngressPreviousResponseID(
					lastTurnPayload,
					currentPayload,
					lastTurnResponseID,
					hasFunctionCallOutput,
				)
			}
			if strictErr != nil {
				logOpenAIWSModeInfo(
					"ingress_ws_prev_response_strict_eval account_id=%d turn=%d conn_id=%s action=keep_previous_response_id reason=%s cause=%s previous_response_id=%s expected_previous_response_id=%s has_function_call_output=%v",
					account.ID,
					turn,
					truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
					normalizeOpenAIWSLogValue(strictReason),
					truncateOpenAIWSLogValue(strictErr.Error(), openAIWSLogValueMaxLen),
					truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
					hasFunctionCallOutput,
				)
			} else if !shouldKeepPreviousResponseID {
				updatedPayload, removed, dropErr := dropPreviousResponseIDFromRawPayload(currentPayload)
				if dropErr != nil || !removed {
					dropReason := "not_removed"
					if dropErr != nil {
						dropReason = "drop_error"
					}
					logOpenAIWSModeInfo(
						"ingress_ws_prev_response_strict_eval account_id=%d turn=%d conn_id=%s action=keep_previous_response_id reason=%s drop_reason=%s previous_response_id=%s expected_previous_response_id=%s has_function_call_output=%v",
						account.ID,
						turn,
						truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
						normalizeOpenAIWSLogValue(strictReason),
						normalizeOpenAIWSLogValue(dropReason),
						truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
						truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
						hasFunctionCallOutput,
					)
				} else {
					// 客户端续链 ID 与当前会话上一轮不一致时，严格降级已经决定
					// 放弃该外部锚点；此时应从当前会话检查点重建完整输入。
					fullInputReady := true
					if expectedPrev != "" && currentPreviousResponseID != expectedPrev {
						strictReplayInput, strictReplayInputExists, strictReplayErr := buildOpenAIWSReplayInputSequence(
							lastTurnReplayInput,
							lastTurnReplayInputExists,
							currentPayload,
							true,
						)
						if strictReplayErr != nil {
							logOpenAIWSModeInfo(
								"ingress_ws_prev_response_strict_eval account_id=%d turn=%d conn_id=%s action=keep_previous_response_id reason=%s drop_reason=build_full_input_error previous_response_id=%s expected_previous_response_id=%s cause=%s has_function_call_output=%v",
								account.ID,
								turn,
								truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
								normalizeOpenAIWSLogValue(strictReason),
								truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
								truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
								truncateOpenAIWSLogValue(strictReplayErr.Error(), openAIWSLogValueMaxLen),
								hasFunctionCallOutput,
							)
							fullInputReady = false
						} else {
							currentTurnReplayInput = strictReplayInput
							currentTurnReplayInputExists = strictReplayInputExists
							currentTurnReplayComplete = lastTurnReplayComplete
							currentTurnReplayReason = lastTurnReplayReason
						}
					}
					if fullInputReady {
						updatedWithInput, setInputErr := setOpenAIWSPayloadInputSequence(
							updatedPayload,
							currentTurnReplayInput,
							currentTurnReplayInputExists,
						)
						if setInputErr != nil {
							logOpenAIWSModeInfo(
								"ingress_ws_prev_response_strict_eval account_id=%d turn=%d conn_id=%s action=keep_previous_response_id reason=%s drop_reason=set_full_input_error previous_response_id=%s expected_previous_response_id=%s cause=%s has_function_call_output=%v",
								account.ID,
								turn,
								truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
								normalizeOpenAIWSLogValue(strictReason),
								truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
								truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
								truncateOpenAIWSLogValue(setInputErr.Error(), openAIWSLogValueMaxLen),
								hasFunctionCallOutput,
							)
						} else {
							currentPayload = updatedWithInput
							logOpenAIWSModeInfo(
								"ingress_ws_prev_response_strict_eval account_id=%d turn=%d conn_id=%s action=drop_previous_response_id_full_create reason=%s previous_response_id=%s expected_previous_response_id=%s has_function_call_output=%v",
								account.ID,
								turn,
								truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
								normalizeOpenAIWSLogValue(strictReason),
								truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
								truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
								hasFunctionCallOutput,
							)
							currentPreviousResponseID = ""
							previousResponseSourceConnID = ""
							currentTurnReplayComplete = true
							currentTurnReplayReason = ""
						}
					}
				}
			}
		}
		forcePreferredConn := isStrictAffinityTurn(currentPayload)
		if sessionLease == nil {
			acquiredLease, acquireErr := acquireTurnLease(turn, preferredConnID, forcePreferredConn, false)
			if acquireErr != nil && forcePreferredConn && errors.Is(acquireErr, errOpenAIWSPreferredConnUnavailable) {
				staleConnID := preferredConnID
				cleanupStaleConnBindings(staleConnID)
				preferredConnID = ""
				acquiredLease, acquireErr = acquireTurnLease(turn, "", false, true)
			}
			if acquireErr != nil {
				return fmt.Errorf("acquire upstream websocket: %w", acquireErr)
			}
			sessionLease = acquiredLease
			sessionConnID = strings.TrimSpace(sessionLease.ConnID())
			if storeDisabled {
				pinSessionConn(sessionConnID)
			} else {
				unpinSessionConn(sessionConnID)
			}
		}
		connID := sessionConnID
		if currentPreviousResponseID != "" {
			chainedFromLast := expectedPrev != "" && currentPreviousResponseID == expectedPrev
			currentPreviousResponseIDKind := ClassifyOpenAIPreviousResponseIDKind(currentPreviousResponseID)
			logOpenAIWSModeInfo(
				"ingress_ws_turn_chain account_id=%d turn=%d conn_id=%s previous_response_id=%s previous_response_id_kind=%s last_turn_response_id=%s chained_from_last=%v preferred_conn_id=%s header_session_id=%s header_conversation_id=%s has_turn_state=%v turn_state_len=%d has_prompt_cache_key=%v store_disabled=%v",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
				normalizeOpenAIWSLogValue(currentPreviousResponseIDKind),
				truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
				chainedFromLast,
				truncateOpenAIWSLogValue(preferredConnID, openAIWSIDValueMaxLen),
				openAIWSHeaderValueForLog(baseAcquireReq.Headers, "session_id"),
				openAIWSHeaderValueForLog(baseAcquireReq.Headers, "conversation_id"),
				turnState != "",
				len(turnState),
				openAIWSPayloadStringFromRaw(currentPayload, "prompt_cache_key") != "",
				storeDisabled,
			)
		}

		// 同连接续链保持原始负载；跨连接时必须先移除连接级 response_id 并补齐完整 input。
		turnPayload := cloneOpenAIWSPayloadBytes(currentPayload)
		preparedPayload, payloadMode, prepareErr := prepareOpenAIWSCrossConnPayload(
			turnPayload,
			previousResponseSourceConnID,
			connID,
			currentTurnReplayInput,
			currentTurnReplayInputExists,
			currentTurnReplayComplete,
			currentTurnReplayReason,
		)
		if prepareErr != nil {
			s.recordOpenAIWSIngressCrossConnBlocked()
			lastTurnClean = true
			logOpenAIWSModeInfo(
				"ingress_ws_cross_conn_replay_blocked account_id=%d turn=%d source_conn_id=%s actual_conn_id=%s previous_response_id=%s reason=%s",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(previousResponseSourceConnID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(prepareErr.Error(), openAIWSLogValueMaxLen),
			)
			return NewOpenAIWSClientCloseError(
				coderws.StatusPolicyViolation,
				"upstream continuation context is unavailable; please restart the conversation",
				prepareErr,
			)
		}
		turnPayload = preparedPayload
		if payloadMode == openAIWSCrossConnPayloadRebuilt {
			s.recordOpenAIWSIngressCrossConnRebuild()
			logOpenAIWSModeInfo(
				"ingress_ws_cross_conn_replay account_id=%d turn=%d source_conn_id=%s actual_conn_id=%s action=drop_previous_response_id_full_create",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(previousResponseSourceConnID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
			)
		}
		result, relayErr := sendAndRelay(turn, sessionLease, turnPayload, len(turnPayload), currentOriginalModel, currentImageBillingModel, currentImageSizeTier, currentImageInputSize, currentFingerprintIDs, currentRequestedReasoningEffort)
		if relayErr != nil && isOpenAIWSIngressTurnRetryable(relayErr) {
			recoveryStartedAt := time.Now()
			s.recordOpenAIWSIngressRecoveryAttempt()
			logOpenAIWSModeInfo(
				"ingress_ws_transport_recovery account_id=%d turn=%d retry=1 reason=%s conn_id=%s",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(openAIWSIngressTurnRetryReason(relayErr), openAIWSLogValueMaxLen),
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
			)
			cleanupStaleConnBindings(connID)
			lastTurnClean = false
			resetSessionLease(true)

			failedConnID := connID
			recoveryLease, acquireErr := acquireTurnLease(turn, "", false, true)
			if acquireErr != nil {
				s.recordOpenAIWSIngressRecoveryFailure()
				relayErr = fmt.Errorf("acquire upstream websocket for transport recovery: %w", acquireErr)
			} else {
				sessionLease = recoveryLease
				sessionConnID = strings.TrimSpace(recoveryLease.ConnID())
				connID = sessionConnID
				if storeDisabled {
					pinSessionConn(sessionConnID)
				}
				recoveryPayload, recoveryMode, recoveryPrepareErr := prepareOpenAIWSCrossConnPayload(
					turnPayload,
					failedConnID,
					connID,
					currentTurnReplayInput,
					currentTurnReplayInputExists,
					currentTurnReplayComplete,
					currentTurnReplayReason,
				)
				if recoveryPrepareErr != nil {
					s.recordOpenAIWSIngressCrossConnBlocked()
					relayErr = NewOpenAIWSClientCloseError(
						coderws.StatusPolicyViolation,
						"upstream continuation context is unavailable; please restart the conversation",
						recoveryPrepareErr,
					)
				} else {
					turnPayload = recoveryPayload
					if recoveryMode == openAIWSCrossConnPayloadRebuilt {
						s.recordOpenAIWSIngressCrossConnRebuild()
						logOpenAIWSModeInfo(
							"ingress_ws_transport_recovery_rebuild account_id=%d turn=%d source_conn_id=%s actual_conn_id=%s action=drop_previous_response_id_full_create",
							account.ID,
							turn,
							truncateOpenAIWSLogValue(failedConnID, openAIWSIDValueMaxLen),
							truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
						)
					}
					result, relayErr = sendAndRelay(turn, sessionLease, turnPayload, len(turnPayload), currentOriginalModel, currentImageBillingModel, currentImageSizeTier, currentImageInputSize, currentFingerprintIDs, currentRequestedReasoningEffort)
				}
				if relayErr == nil {
					s.recordOpenAIWSIngressRecoverySuccess()
				} else {
					s.recordOpenAIWSIngressRecoveryFailure()
				}
			}
			s.recordOpenAIWSIngressRecoveryDuration(time.Since(recoveryStartedAt))
		} else if relayErr != nil && isOpenAIWSIngressTransportFailure(relayErr) {
			s.recordOpenAIWSIngressRecoverySuppressed()
		}
		if relayErr != nil {
			lastTurnClean = false
			if isOpenAIWSSessionPreempted(ctx) {
				sessionLease.MarkBroken()
				return errOpenAIWSSessionPreempted
			}
			var rejectedFieldErr *openAIWSRejectedFieldRetryError
			if errors.As(relayErr, &rejectedFieldErr) && rejectedFieldErr != nil && len(rejectedFieldErr.body) > 0 {
				currentPayload = append([]byte(nil), rejectedFieldErr.body...)
				skipBeforeTurn = true
				continue
			}
			if recoverIngressPrevResponseNotFound(relayErr, turn, connID) {
				continue
			}
			if retryIngressTurn(relayErr, turn, connID) {
				continue
			}
			finalErr := relayErr
			if _, ok := relayErr.(*openAIWSIngressTurnError); ok {
				unwrapped := errors.Unwrap(relayErr)
				finalErr = unwrapped
			}
			if hooks != nil && hooks.AfterTurn != nil {
				hooks.AfterTurn(turn, nil, finalErr)
			}
			if sessionLease != nil {
				sessionLease.MarkBroken()
			}
			return finalErr
		}
		lastTurnClean = true
		turnRetry = 0
		turnPrevRecoveryTried = false
		if result != nil && result.SucceededForScheduling() {
			s.stageOpenAIUserAffinityResponseAlias(ctx, account.ID, result.RequestID)
		}
		if hooks != nil && hooks.AfterTurn != nil {
			hooks.AfterTurn(turn, result, nil)
		}
		if result == nil {
			return errors.New("websocket turn result is nil")
		}
		responseID := strings.TrimSpace(result.RequestID)
		currentPayload = turnPayload
		currentPreviousResponseID = openAIWSPayloadStringFromRaw(turnPayload, "previous_response_id")
		lastTurnResponseID = responseID
		lastTurnPayload = cloneOpenAIWSPayloadBytes(currentPayload)
		lastTurnReplayInput = cloneOpenAIWSRawMessages(currentTurnReplayInput)
		lastTurnReplayInputExists = currentTurnReplayInputExists
		if result.wsReplayInputExists {
			lastTurnReplayInput = append(lastTurnReplayInput, cloneOpenAIWSRawMessages(result.wsReplayInput)...)
			lastTurnReplayInputExists = true
		}
		lastTurnReplayComplete = currentTurnReplayComplete && result.wsReplayComplete
		lastTurnReplayReason = currentTurnReplayReason
		if currentTurnReplayComplete && !result.wsReplayComplete {
			lastTurnReplayReason = result.wsReplayReason
		}
		nextStrictState, strictStateErr := buildOpenAIWSIngressPreviousTurnStrictState(currentPayload)
		if strictStateErr != nil {
			lastTurnStrictState = nil
			logOpenAIWSModeInfo(
				"ingress_ws_prev_response_strict_state_skip account_id=%d turn=%d conn_id=%s reason=build_error cause=%s",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(strictStateErr.Error(), openAIWSLogValueMaxLen),
			)
		} else {
			lastTurnStrictState = nextStrictState
		}

		if responseID != "" && stateStore != nil {
			ttl := s.openAIWSResponseStickyTTL()
			logOpenAIWSBindResponseAccountWarn(groupID, account.ID, responseID, stateStore.BindResponseAccount(ctx, groupID, responseID, account.ID, ttl))
			bindOpenAIWSResponseConn(stateStore, responseID, connectionTarget, connID, ttl)
			requestInput, requestInputSeen, requestInputErr := openAIWSExtractNormalizedInputSequence(turnPayload)
			checkpointReplayable := lastTurnReplayComplete && requestInputErr == nil && result.SucceededForScheduling()
			checkpointReason := lastTurnReplayReason
			if requestInputErr != nil {
				checkpointReason = "request_input_parse_error"
			} else if !result.SucceededForScheduling() {
				checkpointReason = "non_success_terminal_event"
			}
			bindOpenAIWSReplayCheckpoint(stateStore, groupID, responseID, openAIWSReplayCheckpoint{
				SourceConnID:       connID,
				Target:             connectionTarget,
				PreviousResponseID: currentPreviousResponseID,
				RequestInput:       requestInput,
				RequestInputSeen:   requestInputSeen,
				ResponseOutput:     result.wsReplayInput,
				Replayable:         checkpointReplayable,
				UnavailableReason:  checkpointReason,
			}, ttl)
		}
		if stateStore != nil && storeDisabled && sessionHash != "" {
			bindOpenAIWSSessionConn(stateStore, groupID, sessionHash, connectionTarget, connID, s.openAIWSSessionStickyTTL())
		}
		if connID != "" {
			preferredConnID = connID
		}

		nextClientMessage, readErr := readClientMessage()
		if readErr != nil {
			if isOpenAIWSSessionPreempted(ctx) {
				return errOpenAIWSSessionPreempted
			}
			if isOpenAIWSClientDisconnectError(readErr) {
				closeStatus, closeReason := summarizeOpenAIWSReadCloseError(readErr)
				logOpenAIWSModeInfo(
					"ingress_ws_client_closed account_id=%d conn_id=%s close_status=%s close_reason=%s",
					account.ID,
					truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
					closeStatus,
					truncateOpenAIWSLogValue(closeReason, openAIWSHeaderValueMaxLen),
				)
				return nil
			}
			return fmt.Errorf("read client websocket request: %w", readErr)
		}

		nextPayload, parseErr := parseClientPayload(turn+1, nextClientMessage)
		if parseErr != nil {
			return parseErr
		}
		nextRoutingFields := gjson.GetManyBytes(nextPayload.payloadRaw, "model", "service_tier")
		if nextPayload.promptCacheKey != "" {
			// ingress 会话在整个客户端 WS 生命周期内复用同一上游连接；
			// prompt_cache_key 对握手头的更新仅在未来需要重新建连时生效。
			updatedHeaders, _, updHdrErr := s.buildOpenAIWSHeaders(
				ctx,
				c,
				account,
				token,
				wsDecision,
				isCodexCLI,
				turnState,
				strings.TrimSpace(c.GetHeader(openAIWSTurnMetadataHeader)),
				nextPayload.promptCacheKey,
				nextRoutingFields[0].String(),
				nextRoutingFields[1].String(),
			)
			if updHdrErr != nil {
				logOpenAIWSModeInfo("ingress_ws_update_headers_failed account_id=%d err=%v", account.ID, updHdrErr)
			} else {
				baseAcquireReq.Headers = updatedHeaders
			}
		}
		setOpenAICodexRoutingHint(baseAcquireReq.Headers, account, nextRoutingFields[0].String(), nextRoutingFields[1].String())
		if nextPayload.previousResponseID != "" {
			expectedPrev := strings.TrimSpace(lastTurnResponseID)
			chainedFromLast := expectedPrev != "" && nextPayload.previousResponseID == expectedPrev
			nextPreviousResponseIDKind := ClassifyOpenAIPreviousResponseIDKind(nextPayload.previousResponseID)
			logOpenAIWSModeInfo(
				"ingress_ws_next_turn_chain account_id=%d turn=%d next_turn=%d conn_id=%s previous_response_id=%s previous_response_id_kind=%s last_turn_response_id=%s chained_from_last=%v has_prompt_cache_key=%v store_disabled=%v",
				account.ID,
				turn,
				turn+1,
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(nextPayload.previousResponseID, openAIWSIDValueMaxLen),
				normalizeOpenAIWSLogValue(nextPreviousResponseIDKind),
				truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
				chainedFromLast,
				nextPayload.promptCacheKey != "",
				storeDisabled,
			)
		}
		if stateStore != nil && nextPayload.previousResponseID != "" {
			if stickyConnID, ok := getOpenAIWSResponseConn(stateStore, nextPayload.previousResponseID, connectionTarget); ok {
				if sessionConnID != "" && stickyConnID != "" && stickyConnID != sessionConnID {
					logOpenAIWSModeInfo(
						"ingress_ws_keep_session_conn account_id=%d turn=%d conn_id=%s sticky_conn_id=%s previous_response_id=%s",
						account.ID,
						turn,
						truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
						truncateOpenAIWSLogValue(stickyConnID, openAIWSIDValueMaxLen),
						truncateOpenAIWSLogValue(nextPayload.previousResponseID, openAIWSIDValueMaxLen),
					)
				} else {
					preferredConnID = stickyConnID
				}
			}
		}
		currentPayload = nextPayload.payloadRaw
		currentFingerprintIDs = nextPayload.fingerprintIDs
		currentOriginalModel = nextPayload.originalModel
		currentImageBillingModel = nextPayload.imageBillingModel
		currentImageSizeTier = nextPayload.imageSizeTier
		currentImageInputSize = nextPayload.imageInputSize
		currentPayloadBytes = nextPayload.payloadBytes
		currentRequestedReasoningEffort = nextPayload.requestedReasoningEffort
		rejectedFieldRetryState = newOpenAIResponsesRejectedFieldRetryState(currentPayload)
		storeDisabled = s.isOpenAIWSStoreDisabledInRequestRaw(currentPayload, account)
		if !storeDisabled {
			unpinSessionConn(sessionConnID)
		}
		turn++
	}
}
