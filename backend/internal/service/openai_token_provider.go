package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync/atomic"
	"time"
)

const (
	openAITokenRefreshSkew    = 3 * time.Minute
	openAITokenCacheSkew      = 5 * time.Minute
	openAILockInitialWait     = 20 * time.Millisecond
	openAILockMaxWait         = 120 * time.Millisecond
	openAILockMaxAttempts     = 5
	openAILockJitterRatio     = 0.2
	openAILockWarnThresholdMs = 250
)

var (
	// ErrOpenAITokenBindingInvalid 表示请求绑定不能安全地定位到一个账号。
	ErrOpenAITokenBindingInvalid = errors.New("openai token binding is invalid")
	// ErrOpenAIPersonaCredentialChainMissing 表示 Persona 的独立授权链不存在。
	ErrOpenAIPersonaCredentialChainMissing = errors.New("openai persona credential chain is missing")
	// ErrOpenAIPersonaCredentialChainMismatch 表示绑定链 ID 与账号内的链不一致。
	ErrOpenAIPersonaCredentialChainMismatch = errors.New("openai persona credential chain does not match binding")
	// ErrOpenAIPersonaCredentialChainNotReady 表示独立授权链尚未处于可读状态。
	ErrOpenAIPersonaCredentialChainNotReady = errors.New("openai persona credential chain is not ready")
	// ErrOpenAIPersonaAccessTokenMissing 表示独立授权链没有 access_token。
	ErrOpenAIPersonaAccessTokenMissing = errors.New("openai persona access_token is missing")
	// ErrOpenAIPersonaCredentialChainExpired 表示独立授权链的 access_token 已过期。
	ErrOpenAIPersonaCredentialChainExpired = errors.New("openai persona credential chain is expired")
	// ErrOpenAIPersonaCredentialRefreshUnsupported 表示链级刷新尚未安全接入。
	ErrOpenAIPersonaCredentialRefreshUnsupported = errors.New("openai persona credential refresh is not implemented")
)

// OpenAITokenRuntimeMetrics is a snapshot of refresh and lock contention metrics.
type OpenAITokenRuntimeMetrics struct {
	RefreshRequests    int64
	RefreshSuccess     int64
	RefreshFailure     int64
	LockAcquireFailure int64
	LockContention     int64
	LockWaitSamples    int64
	LockWaitTotalMs    int64
	LockWaitHit        int64
	LockWaitMiss       int64
	LastObservedUnixMs int64
}

type openAITokenRuntimeMetricsStore struct {
	refreshRequests    atomic.Int64
	refreshSuccess     atomic.Int64
	refreshFailure     atomic.Int64
	lockAcquireFailure atomic.Int64
	lockContention     atomic.Int64
	lockWaitSamples    atomic.Int64
	lockWaitTotalMs    atomic.Int64
	lockWaitHit        atomic.Int64
	lockWaitMiss       atomic.Int64
	lastObservedUnixMs atomic.Int64
}

func (m *openAITokenRuntimeMetricsStore) snapshot() OpenAITokenRuntimeMetrics {
	if m == nil {
		return OpenAITokenRuntimeMetrics{}
	}
	return OpenAITokenRuntimeMetrics{
		RefreshRequests:    m.refreshRequests.Load(),
		RefreshSuccess:     m.refreshSuccess.Load(),
		RefreshFailure:     m.refreshFailure.Load(),
		LockAcquireFailure: m.lockAcquireFailure.Load(),
		LockContention:     m.lockContention.Load(),
		LockWaitSamples:    m.lockWaitSamples.Load(),
		LockWaitTotalMs:    m.lockWaitTotalMs.Load(),
		LockWaitHit:        m.lockWaitHit.Load(),
		LockWaitMiss:       m.lockWaitMiss.Load(),
		LastObservedUnixMs: m.lastObservedUnixMs.Load(),
	}
}

func (m *openAITokenRuntimeMetricsStore) touchNow() {
	if m == nil {
		return
	}
	m.lastObservedUnixMs.Store(time.Now().UnixMilli())
}

// OpenAITokenCache token cache interface.
type OpenAITokenCache = GeminiTokenCache

// OpenAITokenProvider manages access_token for OpenAI OAuth accounts.
type OpenAITokenProvider struct {
	accountRepo        AccountRepository
	tokenCache         OpenAITokenCache
	openAIOAuthService *OpenAIOAuthService
	runtimeBlocker     AccountRuntimeBlocker
	metrics            *openAITokenRuntimeMetricsStore
	refreshAPI         *OAuthRefreshAPI
	executor           OAuthRefreshExecutor
	refreshPolicy      ProviderRefreshPolicy
}

func NewOpenAITokenProvider(
	accountRepo AccountRepository,
	tokenCache OpenAITokenCache,
	openAIOAuthService *OpenAIOAuthService,
) *OpenAITokenProvider {
	return &OpenAITokenProvider{
		accountRepo:        accountRepo,
		tokenCache:         tokenCache,
		openAIOAuthService: openAIOAuthService,
		metrics:            &openAITokenRuntimeMetricsStore{},
		refreshPolicy:      OpenAIProviderRefreshPolicy(),
	}
}

// SetRefreshAPI injects unified OAuth refresh API and executor.
func (p *OpenAITokenProvider) SetRefreshAPI(api *OAuthRefreshAPI, executor OAuthRefreshExecutor) {
	p.refreshAPI = api
	p.executor = executor
}

// SetRefreshPolicy injects caller-side refresh policy.
func (p *OpenAITokenProvider) SetRefreshPolicy(policy ProviderRefreshPolicy) {
	p.refreshPolicy = policy
}

func (p *OpenAITokenProvider) SetAccountRuntimeBlocker(blocker AccountRuntimeBlocker) {
	p.runtimeBlocker = blocker
}

func (p *OpenAITokenProvider) SnapshotRuntimeMetrics() OpenAITokenRuntimeMetrics {
	if p == nil {
		return OpenAITokenRuntimeMetrics{}
	}
	p.ensureMetrics()
	return p.metrics.snapshot()
}

func (p *OpenAITokenProvider) ensureMetrics() {
	if p != nil && p.metrics == nil {
		p.metrics = &openAITokenRuntimeMetricsStore{}
	}
}

// GetAccessToken returns a valid access_token.
func (p *OpenAITokenProvider) GetAccessToken(ctx context.Context, account *Account) (string, error) {
	p.ensureMetrics()
	if account == nil {
		return "", errors.New("account is nil")
	}
	if account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
		return "", errors.New("not an openai oauth account")
	}

	cacheKey := OpenAITokenCacheKey(account)

	// 1) Try cache first.
	if p.tokenCache != nil {
		if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
			slog.Debug("openai_token_cache_hit", "account_id", account.ID)
			return token, nil
		} else if err != nil {
			slog.Warn("openai_token_cache_get_failed", "account_id", account.ID, "error", err)
		}
	}

	slog.Debug("openai_token_cache_miss", "account_id", account.ID)
	credentialsAbsent := strings.TrimSpace(account.GetOpenAIAccessToken()) == ""
	if account.IsSchedulerSnapshot && p.accountRepo == nil {
		return "", errors.New("authoritative account repository is required for an OpenAI scheduler snapshot")
	}
	if (account.IsSchedulerSnapshot || credentialsAbsent) && p.accountRepo != nil {
		slog.Warn("openai_token_provider.scheduler_snapshot_incomplete",
			"account_id", account.ID,
			"is_scheduler_snapshot", account.IsSchedulerSnapshot,
			"action", "reload_authoritative_account",
		)
		authoritative, err := p.accountRepo.GetByID(ctx, account.ID)
		if err != nil {
			return "", fmt.Errorf("load authoritative OpenAI account %d: %w", account.ID, err)
		}
		if authoritative == nil {
			return "", fmt.Errorf("authoritative OpenAI account %d not found", account.ID)
		}
		if !authoritative.IsOpenAIOAuth() {
			return "", fmt.Errorf("authoritative account %d is not an OpenAI OAuth account", account.ID)
		}
		account = authoritative
	}

	// 2) Refresh if needed (pre-expiry skew).
	expiresAt := account.GetCredentialAsTime("expires_at")
	needsRefresh := !account.IsOpenAIPersonalAccessToken() && (expiresAt == nil || time.Until(*expiresAt) <= openAITokenRefreshSkew)
	if needsRefresh && strings.TrimSpace(account.GetOpenAIRefreshToken()) == "" {
		if expiresAt != nil && !time.Now().Before(*expiresAt) {
			const reason = "openai access_token expired and refresh_token is missing"
			// 永久故障：缺失 refresh_token 时账号无法自愈，必须立即从调度池剔除，
			// 否则会被反复选中、每次都在 token 阶段直接返回错误，对用户呈现持续 502。
			p.disableAccountMissingRefreshToken(account, reason)
			return "", errors.New(reason)
		}
		needsRefresh = false
	}
	refreshFailed := false

	if needsRefresh && p.refreshAPI != nil && p.executor != nil {
		p.metrics.refreshRequests.Add(1)
		p.metrics.touchNow()

		result, err := p.refreshAPI.RefreshIfNeeded(ctx, account, p.executor, openAITokenRefreshSkew)
		if err != nil {
			if p.refreshPolicy.OnRefreshError == ProviderRefreshErrorReturn {
				return "", err
			}
			slog.Warn("openai_token_refresh_failed", "account_id", account.ID, "error", err)
			p.metrics.refreshFailure.Add(1)
			refreshFailed = true
		} else if result.LockHeld {
			if p.refreshPolicy.OnLockHeld == ProviderLockHeldWaitForCache {
				p.metrics.lockContention.Add(1)
				p.metrics.touchNow()
				token, waitErr := p.waitForTokenAfterLockRace(ctx, cacheKey)
				if waitErr != nil {
					return "", waitErr
				}
				if strings.TrimSpace(token) != "" {
					slog.Debug("openai_token_cache_hit_after_wait", "account_id", account.ID)
					return token, nil
				}
			}
		} else if result.Refreshed {
			p.metrics.refreshSuccess.Add(1)
			account = result.Account
			expiresAt = account.GetCredentialAsTime("expires_at")
		} else {
			account = result.Account
			expiresAt = account.GetCredentialAsTime("expires_at")
		}
	} else if needsRefresh && p.tokenCache != nil {
		// Backward-compatible test path when refreshAPI is not injected.
		p.metrics.refreshRequests.Add(1)
		p.metrics.touchNow()
		locked, lockErr := p.tokenCache.AcquireRefreshLock(ctx, cacheKey, 30*time.Second)
		if lockErr == nil && locked {
			defer func() { _ = p.tokenCache.ReleaseRefreshLock(ctx, cacheKey) }()
		} else if lockErr != nil {
			p.metrics.lockAcquireFailure.Add(1)
			p.metrics.touchNow()
			slog.Warn("openai_token_lock_failed", "account_id", account.ID, "error", lockErr)
		} else {
			p.metrics.lockContention.Add(1)
			p.metrics.touchNow()
			token, waitErr := p.waitForTokenAfterLockRace(ctx, cacheKey)
			if waitErr != nil {
				return "", waitErr
			}
			if strings.TrimSpace(token) != "" {
				slog.Debug("openai_token_cache_hit_after_wait", "account_id", account.ID)
				return token, nil
			}
		}
	}

	accessToken := account.GetCredential("access_token")
	if strings.TrimSpace(accessToken) == "" {
		return "", errors.New("access_token not found in credentials")
	}

	// 3) Populate cache with TTL.
	if p.tokenCache != nil {
		latestAccount, isStale := CheckTokenVersion(ctx, account, p.accountRepo)
		if isStale && latestAccount != nil {
			slog.Debug("openai_token_version_stale_use_latest", "account_id", account.ID)
			accessToken = latestAccount.GetOpenAIAccessToken()
			if strings.TrimSpace(accessToken) == "" {
				return "", errors.New("access_token not found after version check")
			}
		} else {
			ttl := 30 * time.Minute
			if refreshFailed {
				if p.refreshPolicy.FailureTTL > 0 {
					ttl = p.refreshPolicy.FailureTTL
				} else {
					ttl = time.Minute
				}
				slog.Debug("openai_token_cache_short_ttl", "account_id", account.ID, "reason", "refresh_failed")
			} else if expiresAt != nil {
				until := time.Until(*expiresAt)
				switch {
				case until > openAITokenCacheSkew:
					ttl = until - openAITokenCacheSkew
				case until > 0:
					ttl = until
				default:
					ttl = time.Minute
				}
			}
			if err := p.tokenCache.SetAccessToken(ctx, cacheKey, accessToken, ttl); err != nil {
				slog.Warn("openai_token_cache_set_failed", "account_id", account.ID, "error", err)
			}
		}
	}

	return accessToken, nil
}

// GetAccessTokenForBinding 按 Account×Persona×credential chain 读取 access_token。
// v1/v2 以及明确的 legacy-codex 绑定保持旧账号级路径；v3 的显式链（包括
// strict Codex 与 OpenCode）只读取绑定链自己的凭据和命名空间缓存，不会回退到
// 其他 Persona 的 refresh_token，也不会把链级刷新伪装成账号级刷新。
func (p *OpenAITokenProvider) GetAccessTokenForBinding(
	ctx context.Context,
	account *Account,
	binding SessionPersonaSlotBinding,
) (string, error) {
	if p == nil {
		return "", errors.New("openai token provider is nil")
	}
	p.ensureMetrics()
	if account == nil {
		return "", errors.New("account is nil")
	}
	if account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
		return "", errors.New("not an openai oauth account")
	}

	// 允许调用方只填写兼容别名 Persona 字段，但一旦存在账号 ID，
	// 必须与实际取 token 的账号一致，避免错误绑定导致跨账号读取。
	personaRaw := strings.TrimSpace(string(binding.PersonaID))
	if personaRaw == "" {
		personaRaw = strings.TrimSpace(string(binding.Persona.ID))
	}
	persona, ok := ParseSessionPersonaID(personaRaw)
	if !ok {
		return "", fmt.Errorf("%w: persona=%q", ErrOpenAITokenBindingInvalid, personaRaw)
	}
	binding.PersonaID = persona
	binding.CredentialChainID = strings.TrimSpace(binding.CredentialChainID)
	if binding.AccountID != 0 && binding.AccountID != account.ID {
		return "", fmt.Errorf("%w: account_id=%d does not match account %d", ErrOpenAITokenBindingInvalid, binding.AccountID, account.ID)
	}

	// v1/v2 以及显式 legacy-codex 的 strict Codex 绑定必须沿用旧账号级
	// refresh/cache 语义。这样历史双 Codex 账号不会因为新增入口而改变行为；
	// 一旦绑定带有其他 chain ID，则进入 Account×Persona×chain 读取路径。
	if persona == SessionPersonaCodexCLIStrict &&
		(binding.Legacy || binding.EffectiveMappingVersion() < SessionPersonaScopeVersionV3 ||
			(binding.SlotID == 0 && (binding.CredentialChainID == "" || binding.CredentialChainID == "legacy-codex"))) {
		return p.GetAccessToken(ctx, account)
	}

	if !binding.Valid() {
		return "", fmt.Errorf("%w: persona=%q slot=%d scope=%d", ErrOpenAITokenBindingInvalid, persona, binding.SlotID, binding.ScopeVersion)
	}
	if persona != SessionPersonaCodexCLIStrict && persona != SessionPersonaOpenCode {
		return "", fmt.Errorf("%w: unsupported persona=%q", ErrOpenAITokenBindingInvalid, persona)
	}
	if binding.CredentialChainID == "" {
		return "", fmt.Errorf("%w: persona=%q slot=%d", ErrOpenAIPersonaCredentialChainMissing, persona, binding.SlotID)
	}
	// Scheduler selections are intentionally lightweight snapshots and may not
	// carry nested Account×Persona credential chains. Reload the authoritative
	// row before resolving the requested Persona chain; never fall back to the
	// account-level Codex row merely because the snapshot is incomplete.
	if account.IsSchedulerSnapshot || account.findPersonaCredentialByChainID(persona, binding.SlotID, binding.CredentialChainID) == nil {
		if p.accountRepo == nil {
			if account.IsSchedulerSnapshot {
				return "", fmt.Errorf("authoritative account repository is required for %s scheduler snapshot", persona)
			}
		} else {
			authoritative, err := p.accountRepo.GetByID(ctx, account.ID)
			if err != nil {
				return "", fmt.Errorf("load authoritative %s account %d: %w", persona, account.ID, err)
			}
			if authoritative == nil || authoritative.ID != account.ID || !authoritative.IsOpenAIOAuth() {
				return "", fmt.Errorf("authoritative account %d is not an OpenAI OAuth account", account.ID)
			}
			account = authoritative
		}
	}
	return p.getPersonaAccessTokenForBinding(ctx, account, binding, persona)
}

// getPersonaAccessTokenForBinding 只访问绑定的 Persona 链。临近过期时使用
// 账号级凭据写锁重新加载权威行，再刷新并持久化同一条 chain；不会调用旧的
// 账号级 RefreshIfNeeded，也不会借用其他 Persona 的 refresh_token。
func (p *OpenAITokenProvider) getPersonaAccessTokenForBinding(
	ctx context.Context,
	account *Account,
	binding SessionPersonaSlotBinding,
	persona SessionPersonaID,
) (string, error) {
	if binding.CredentialChainID == "" {
		return "", fmt.Errorf("%w: persona=%q slot=%d", ErrOpenAIPersonaCredentialChainMissing, binding.PersonaID, binding.SlotID)
	}

	// A slot may retain more than one chain during OAuth rotation. Resolve by
	// the requested chain ID first; selecting the first slot match would make
	// map iteration order part of credential routing.
	chain := account.findPersonaCredentialByChainID(persona, binding.SlotID, binding.CredentialChainID)
	if chain == nil {
		candidate := account.findPersonaCredential(persona, binding.SlotID)
		if candidate == nil {
			return "", fmt.Errorf("%w: persona=%q slot=%d chain=%q", ErrOpenAIPersonaCredentialChainMissing, persona, binding.SlotID, binding.CredentialChainID)
		}
		return "", fmt.Errorf("%w: persona=%q slot=%d requested_chain=%q stored_chain=%q", ErrOpenAIPersonaCredentialChainMismatch, persona, binding.SlotID, binding.CredentialChainID, strings.TrimSpace(openAIMapString(candidate, "credential_chain_id")))
	}
	if storedChainID := strings.TrimSpace(openAIMapString(chain, "credential_chain_id")); storedChainID == "" || storedChainID != binding.CredentialChainID {
		return "", fmt.Errorf("%w: persona=%q slot=%d requested_chain=%q stored_chain=%q", ErrOpenAIPersonaCredentialChainMismatch, persona, binding.SlotID, binding.CredentialChainID, storedChainID)
	}
	if chainPersona := strings.TrimSpace(openAIMapString(chain, "persona")); chainPersona != "" {
		parsedPersona, personaOK := ParseSessionPersonaID(chainPersona)
		if !personaOK || parsedPersona != persona {
			return "", fmt.Errorf("%w: chain=%q has persona=%q", ErrOpenAIPersonaCredentialChainMismatch, binding.CredentialChainID, chainPersona)
		}
	}
	if chainAccountID := strings.TrimSpace(openAIMapString(chain, "chatgpt_account_id")); chainAccountID != "" {
		accountID := strings.TrimSpace(account.GetChatGPTAccountID())
		if accountID != "" && chainAccountID != accountID {
			return "", fmt.Errorf("%w: chain=%q belongs to chatgpt account %q, account is %q", ErrOpenAIPersonaCredentialChainMismatch, binding.CredentialChainID, chainAccountID, accountID)
		}
	}
	if rawSlot, hasSlot := chain["slot_id"]; hasSlot && parseSessionPersonaInt64(rawSlot) != int64(binding.SlotID) {
		return "", fmt.Errorf("%w: chain=%q has slot=%d, binding slot=%d", ErrOpenAIPersonaCredentialChainMismatch, binding.CredentialChainID, parseSessionPersonaInt64(rawSlot), binding.SlotID)
	}
	if chainInstallationID := strings.TrimSpace(openAIMapString(chain, "installation_id")); chainInstallationID != "" && strings.TrimSpace(binding.InstallationID) != "" &&
		chainInstallationID != strings.TrimSpace(binding.InstallationID) {
		return "", fmt.Errorf("%w: chain=%q has installation_id=%q, binding has %q", ErrOpenAIPersonaCredentialChainMismatch, binding.CredentialChainID, chainInstallationID, strings.TrimSpace(binding.InstallationID))
	}
	if !openAIPersonaCredentialReady(chain) {
		return "", fmt.Errorf("%w: persona=%q slot=%d chain=%q", ErrOpenAIPersonaCredentialChainNotReady, persona, binding.SlotID, binding.CredentialChainID)
	}

	expiresAt := openAIPersonaCredentialExpiry(chain)
	needsRefresh := expiresAt == nil || time.Until(*expiresAt) <= openAITokenRefreshSkew
	if needsRefresh && strings.TrimSpace(openAIMapString(chain, "refresh_token")) == "" {
		return "", fmt.Errorf("%w: chain=%q has no independent refresh_token", ErrOpenAIPersonaCredentialChainExpired, binding.CredentialChainID)
	}

	cacheKey := OpenAITokenCacheKeyForBinding(account, binding)
	if p.tokenCache != nil {
		if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
			slog.Debug("openai_persona_token_cache_hit", "account_id", account.ID, "persona", binding.PersonaID, "slot_id", binding.SlotID)
			return token, nil
		} else if err != nil {
			slog.Warn("openai_persona_token_cache_get_failed", "account_id", account.ID, "persona", binding.PersonaID, "slot_id", binding.SlotID, "error", err)
		}
	}
	if needsRefresh {
		return p.refreshPersonaAccessToken(ctx, account, binding, persona, cacheKey)
	}

	// Cache 命中仍以命名空间为边界；没有缓存时只允许读取本链 access_token。
	accessToken := strings.TrimSpace(openAIMapString(chain, "access_token"))
	if accessToken == "" {
		return "", fmt.Errorf("%w: persona=%q slot=%d chain=%q", ErrOpenAIPersonaAccessTokenMissing, persona, binding.SlotID, binding.CredentialChainID)
	}

	if p.tokenCache != nil {
		ttl := 30 * time.Minute
		if expiresAt != nil {
			until := time.Until(*expiresAt)
			switch {
			case until > openAITokenCacheSkew:
				ttl = until - openAITokenCacheSkew
			case until > 0:
				ttl = until
			default:
				// The expiry check above should have caught this. Keep the guard
				// explicit so a clock boundary cannot create a stale cache entry.
				return "", fmt.Errorf("%w: chain=%q", ErrOpenAIPersonaCredentialChainExpired, binding.CredentialChainID)
			}
		}
		if err := p.tokenCache.SetAccessToken(ctx, cacheKey, accessToken, ttl); err != nil {
			slog.Warn("openai_persona_token_cache_set_failed", "account_id", account.ID, "persona", binding.PersonaID, "slot_id", binding.SlotID, "error", err)
		}
	}

	return accessToken, nil
}

func (p *OpenAITokenProvider) refreshPersonaAccessToken(
	ctx context.Context,
	account *Account,
	binding SessionPersonaSlotBinding,
	persona SessionPersonaID,
	cacheKey string,
) (string, error) {
	if p.accountRepo == nil || p.openAIOAuthService == nil {
		return "", fmt.Errorf("%w: authoritative repository or OAuth service unavailable", ErrOpenAIPersonaCredentialRefreshUnsupported)
	}
	lockKey := OpenAITokenCacheKey(account) + ":persona-credentials-write"
	locked := false
	if p.tokenCache != nil {
		var err error
		locked, err = p.tokenCache.AcquireRefreshLock(ctx, lockKey, 30*time.Second)
		if err != nil {
			return "", fmt.Errorf("acquire OpenAI Persona credential write lock: %w", err)
		}
		if !locked {
			if token, waitErr := p.waitForTokenAfterLockRace(ctx, cacheKey); waitErr == nil && strings.TrimSpace(token) != "" {
				return token, nil
			}
			return "", fmt.Errorf("%w: credential write is already in progress for account %d", ErrOpenAIPersonaCredentialRefreshUnsupported, account.ID)
		}
		defer func() { _ = p.tokenCache.ReleaseRefreshLock(context.Background(), lockKey) }()
	}

	fresh, err := p.accountRepo.GetByID(ctx, account.ID)
	if err != nil {
		return "", fmt.Errorf("reload authoritative OpenAI Persona account %d: %w", account.ID, err)
	}
	if fresh == nil || !fresh.IsOpenAIOAuth() {
		return "", fmt.Errorf("authoritative account %d is not an OpenAI OAuth account", account.ID)
	}
	chain := fresh.findPersonaCredentialByChainID(persona, binding.SlotID, binding.CredentialChainID)
	if chain == nil || !openAIPersonaCredentialReady(chain) {
		return "", fmt.Errorf("%w: chain=%q", ErrOpenAIPersonaCredentialChainNotReady, binding.CredentialChainID)
	}
	expiresAt := openAIPersonaCredentialExpiry(chain)
	accessToken := strings.TrimSpace(openAIMapString(chain, "access_token"))
	if expiresAt != nil && time.Until(*expiresAt) > openAITokenRefreshSkew && accessToken != "" {
		return p.cachePersonaAccessToken(ctx, cacheKey, accessToken, expiresAt)
	}

	info, credentials, err := p.openAIOAuthService.RefreshPersonaCredential(ctx, fresh, binding)
	if err != nil {
		return "", err
	}
	if err := persistAccountCredentials(ctx, p.accountRepo, fresh, credentials); err != nil {
		return "", fmt.Errorf("persist OpenAI Persona credential chain %q: %w", binding.CredentialChainID, err)
	}
	accessToken = strings.TrimSpace(info.AccessToken)
	if accessToken == "" {
		return "", fmt.Errorf("%w: chain=%q", ErrOpenAIPersonaAccessTokenMissing, binding.CredentialChainID)
	}
	refreshedExpiry := time.Unix(info.ExpiresAt, 0)
	return p.cachePersonaAccessToken(ctx, cacheKey, accessToken, &refreshedExpiry)
}

func (p *OpenAITokenProvider) cachePersonaAccessToken(ctx context.Context, cacheKey, accessToken string, expiresAt *time.Time) (string, error) {
	if p.tokenCache == nil {
		return accessToken, nil
	}
	ttl := 30 * time.Minute
	if expiresAt != nil {
		until := time.Until(*expiresAt)
		switch {
		case until > openAITokenCacheSkew:
			ttl = until - openAITokenCacheSkew
		case until > 0:
			ttl = until
		default:
			return "", ErrOpenAIPersonaCredentialChainExpired
		}
	}
	if err := p.tokenCache.SetAccessToken(ctx, cacheKey, accessToken, ttl); err != nil {
		slog.Warn("openai_persona_token_cache_set_failed", "cache_key", cacheKey, "error", err)
	}
	return accessToken, nil
}

// openAIPersonaCredentialReady interprets the optional readiness/state fields
// used by imported credential-chain records without mutating the account map.
func openAIPersonaCredentialReady(chain map[string]any) bool {
	if chain == nil {
		return false
	}
	if ready, ok := chain["ready"]; ok {
		switch value := ready.(type) {
		case bool:
			if !value {
				return false
			}
		case string:
			if strings.EqualFold(strings.TrimSpace(value), "false") || strings.TrimSpace(value) == "0" {
				return false
			}
		}
	}
	switch strings.ToLower(strings.TrimSpace(openAIMapString(chain, "state"))) {
	case "", "ready":
		return true
	case "pending", "refreshing", "invalid", "revoked", "disabled":
		return false
	default:
		// Unknown state is not treated as ready. Imported records must opt in
		// explicitly once a state vocabulary is extended.
		return false
	}
}

func openAIPersonaCredentialExpiry(chain map[string]any) *time.Time {
	if chain == nil {
		return nil
	}
	if raw, ok := chain["expires_at"]; ok {
		switch value := raw.(type) {
		case time.Time:
			copy := value
			return &copy
		case *time.Time:
			if value != nil {
				copy := *value
				return &copy
			}
		}
	}
	// Reuse the account-level tolerant parser for RFC3339 and Unix seconds;
	// this temporary wrapper only aliases the read-only map and never persists it.
	return (&Account{Credentials: chain}).GetCredentialAsTime("expires_at")
}

// findPersonaCredentialByChainID resolves the exact chain in the account's
// nested credential stores. It mirrors the supported imported shapes used by
// the Persona resolver while adding the chain dimension required during
// rotation.
func (a *Account) findPersonaCredentialByChainID(
	persona SessionPersonaID,
	slotID int,
	chainID string,
) map[string]any {
	if a == nil || a.Credentials == nil || slotID < 0 || strings.TrimSpace(chainID) == "" {
		return nil
	}
	canonical, ok := ParseSessionPersonaID(string(persona))
	if !ok {
		return nil
	}
	chainID = strings.TrimSpace(chainID)
	for _, key := range []string{openAIPersonaCredentialsKey, openAIOAuthCredentialChainsKey} {
		raw, exists := a.Credentials[key]
		if !exists {
			continue
		}
		if found := findPersonaCredentialByChainIDInValue(raw, canonical, slotID, chainID); found != nil {
			return found
		}
	}
	return nil
}

func findPersonaCredentialByChainIDInValue(
	raw any,
	persona SessionPersonaID,
	slotID int,
	chainID string,
) map[string]any {
	switch value := raw.(type) {
	case map[string]any:
		if candidateChainID, hasChain := credentialChainIDFromMap(value); hasChain {
			candidatePersona, hasPersona := credentialPersonaFromMap(value)
			candidateSlot, hasSlot := credentialSlotIDFromMap(value)
			if hasPersona && hasSlot && candidatePersona == persona && candidateSlot == slotID && candidateChainID == chainID {
				return value
			}
		}
		for _, item := range value {
			if found := findPersonaCredentialByChainIDInValue(item, persona, slotID, chainID); found != nil {
				return found
			}
		}
	case map[string]string:
		converted := make(map[string]any, len(value))
		for key, item := range value {
			converted[key] = item
		}
		return findPersonaCredentialByChainIDInValue(converted, persona, slotID, chainID)
	case map[int]any:
		for _, item := range value {
			if found := findPersonaCredentialByChainIDInValue(item, persona, slotID, chainID); found != nil {
				return found
			}
		}
	case map[int]string:
		for _, item := range value {
			if found := findPersonaCredentialByChainIDInValue(item, persona, slotID, chainID); found != nil {
				return found
			}
		}
	case []any:
		for _, item := range value {
			if found := findPersonaCredentialByChainIDInValue(item, persona, slotID, chainID); found != nil {
				return found
			}
		}
	case string:
		trimmed := strings.TrimSpace(value)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var decoded any
			if json.Unmarshal([]byte(trimmed), &decoded) == nil {
				return findPersonaCredentialByChainIDInValue(decoded, persona, slotID, chainID)
			}
		}
	case json.RawMessage:
		var decoded any
		if json.Unmarshal(value, &decoded) == nil {
			return findPersonaCredentialByChainIDInValue(decoded, persona, slotID, chainID)
		}
	}
	return nil
}

// disableAccountMissingRefreshToken 在请求路径上发现 OpenAI OAuth 账号
// 凭证已过期且 refresh_token 缺失时，将账号标记为 error 状态。
// 这是一种永久性故障：仅靠后续请求或 TokenRefreshService 不会自愈
// （NeedsRefresh 也会因 refresh_token 为空直接跳过），
// 必须主动剔除以避免账号被持续选中导致用户端反复 502。
// 使用 background context 是因为请求 context 可能很快结束。
func (p *OpenAITokenProvider) disableAccountMissingRefreshToken(account *Account, reason string) {
	if p == nil || p.accountRepo == nil || account == nil {
		return
	}
	if p.runtimeBlocker != nil {
		p.runtimeBlocker.BlockAccountScheduling(account, time.Time{}, "missing_refresh_token")
	}
	bgCtx := context.Background()
	if err := p.accountRepo.SetError(bgCtx, account.ID, reason); err != nil {
		slog.Warn("openai_token_provider.set_error_failed",
			"account_id", account.ID,
			"error", err,
		)
		return
	}
	if p.tokenCache != nil {
		cacheKey := OpenAITokenCacheKey(account)
		if err := p.tokenCache.DeleteAccessToken(bgCtx, cacheKey); err != nil {
			slog.Warn("openai_token_provider.cache_delete_failed",
				"account_id", account.ID,
				"error", err,
			)
		}
	}
	slog.Warn("openai_token_provider.account_disabled_missing_refresh_token",
		"account_id", account.ID,
		"reason", reason,
	)
}

func (p *OpenAITokenProvider) waitForTokenAfterLockRace(ctx context.Context, cacheKey string) (string, error) {
	wait := openAILockInitialWait
	totalWaitMs := int64(0)
	for i := 0; i < openAILockMaxAttempts; i++ {
		actualWait := jitterLockWait(wait)
		timer := time.NewTimer(actualWait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return "", ctx.Err()
		case <-timer.C:
		}

		waitMs := actualWait.Milliseconds()
		if waitMs < 0 {
			waitMs = 0
		}
		totalWaitMs += waitMs
		p.metrics.lockWaitSamples.Add(1)
		p.metrics.lockWaitTotalMs.Add(waitMs)
		p.metrics.touchNow()

		token, err := p.tokenCache.GetAccessToken(ctx, cacheKey)
		if err == nil && strings.TrimSpace(token) != "" {
			p.metrics.lockWaitHit.Add(1)
			if totalWaitMs >= openAILockWarnThresholdMs {
				slog.Warn("openai_token_lock_wait_high", "wait_ms", totalWaitMs, "attempts", i+1)
			}
			return token, nil
		}

		if wait < openAILockMaxWait {
			wait *= 2
			if wait > openAILockMaxWait {
				wait = openAILockMaxWait
			}
		}
	}

	p.metrics.lockWaitMiss.Add(1)
	if totalWaitMs >= openAILockWarnThresholdMs {
		slog.Warn("openai_token_lock_wait_high", "wait_ms", totalWaitMs, "attempts", openAILockMaxAttempts)
	}
	return "", nil
}

func jitterLockWait(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	minFactor := 1 - openAILockJitterRatio
	maxFactor := 1 + openAILockJitterRatio
	factor := minFactor + rand.Float64()*(maxFactor-minFactor)
	return time.Duration(float64(base) * factor)
}
