package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenAIPersonaIDMappingType identifies the client/upstream identifier pair.
// The response and thread mappings are the minimum state required for safe
// OpenCode continuation; the other types reserve the same durable scope for
// later message/compaction projections without changing the lookup contract.
type OpenAIPersonaIDMappingType string

const (
	OpenAIPersonaMappingThread     OpenAIPersonaIDMappingType = "thread"
	OpenAIPersonaMappingResponse   OpenAIPersonaIDMappingType = "response"
	OpenAIPersonaMappingMessage    OpenAIPersonaIDMappingType = "message"
	OpenAIPersonaMappingCompaction OpenAIPersonaIDMappingType = "compaction"
	OpenAIPersonaMappingToolCall   OpenAIPersonaIDMappingType = "tool_call"
)

const (
	openAIPersonaMappingStatusActive   = "active"
	openAIPersonaMappingStatusDraining = "draining"
	openAIPersonaMappingStatusExpired  = "expired"
	openAIPersonaMappingStatusRevoked  = "revoked"
	openAIPersonaMappingResponseKey    = "openai_opencode_response_mapping"
)

// OpenAIPersonaIDMappingScope is the complete non-secret identity boundary
// for an Account×Persona×Slot×Epoch×Credential Chain mapping.
type OpenAIPersonaIDMappingScope struct {
	UserID            int64
	APIKeyID          int64
	AccountID         int64
	AccountPersonaID  int64
	ScopeKey          string
	Persona           SessionPersonaID
	ProfileVersion    string
	SlotID            int
	SessionEpoch      int64
	PersonaGeneration int64
	SlotGeneration    int64
	SlotSetGeneration int64
	CredentialChainID string
	ThreadID          string
}

// OpenAIPersonaIDMapping stores one client ID and its Persona-native OpenCode
// counterpart. IDs are opaque and never include tokens or authorization data.
type OpenAIPersonaIDMapping struct {
	ID              int64
	Scope           OpenAIPersonaIDMappingScope
	MappingType     OpenAIPersonaIDMappingType
	ClientID        string
	OpenCodeID      string
	Status          string
	ParentMappingID *int64
	RootMappingID   *int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastSeenAt      time.Time
	ExpiresAt       time.Time
}

// OpenAIPersonaIDMappingStore is deliberately separate from AccountRepository
// so old mocks and non-PostgreSQL test repositories remain source compatible.
type OpenAIPersonaIDMappingStore interface {
	GetOpenAIPersonaIDMappingByClient(ctx context.Context, scope OpenAIPersonaIDMappingScope, mappingType OpenAIPersonaIDMappingType, clientID string) (*OpenAIPersonaIDMapping, error)
	GetOpenAIPersonaIDMappingByOpenCode(ctx context.Context, scope OpenAIPersonaIDMappingScope, mappingType OpenAIPersonaIDMappingType, openCodeID string) (*OpenAIPersonaIDMapping, error)
	FindOpenAIPersonaIDMappingByPrincipal(ctx context.Context, userID, apiKeyID int64, mappingType OpenAIPersonaIDMappingType, clientID string) (*OpenAIPersonaIDMapping, error)
	UpsertOpenAIPersonaIDMapping(ctx context.Context, mapping *OpenAIPersonaIDMapping) (*OpenAIPersonaIDMapping, error)
}

var (
	ErrOpenAIPersonaIDMappingUnavailable = errors.New("OpenAI Persona ID mapping storage unavailable")
	ErrOpenAIPersonaIDMappingNotFound    = sql.ErrNoRows
	ErrOpenAIPersonaIDMappingConflict    = errors.New("OpenAI Persona ID mapping conflicts with an existing identity")
)

func openAIPersonaIDMappingStore(s *OpenAIGatewayService) OpenAIPersonaIDMappingStore {
	if s == nil {
		return nil
	}
	if s.personaIDMappingStore != nil {
		return s.personaIDMappingStore
	}
	return openAIPersonaIDMappingStoreFromRepository(s.accountRepo)
}

func openAIPersonaIDMappingStoreFromRepository(repo AccountRepository) OpenAIPersonaIDMappingStore {
	if repo == nil {
		return nil
	}
	store, _ := repo.(OpenAIPersonaIDMappingStore)
	return store
}

func openAIPersonaOwnerFromGin(c *gin.Context) (int64, int64) {
	if c == nil {
		return 0, 0
	}
	if raw, ok := c.Get(openAIHTTPResponseOwnerContextKey); ok {
		if owner, ok := raw.(openAIHTTPResponseOwner); ok {
			return owner.userID, owner.apiKeyID
		}
	}
	return 0, getAPIKeyIDFromContext(c)
}

// OpenAIPersonaIDMappingScopeForBinding constructs the durable mapping scope.
func OpenAIPersonaIDMappingScopeForBinding(c *gin.Context, binding SessionPersonaSlotBinding) OpenAIPersonaIDMappingScope {
	binding = binding.NormalizeLifecycle()
	userID, apiKeyID := openAIPersonaOwnerFromGin(c)
	return OpenAIPersonaIDMappingScope{
		UserID:            userID,
		APIKeyID:          apiKeyID,
		AccountID:         binding.AccountID,
		AccountPersonaID:  binding.AccountPersonaID,
		ScopeKey:          SessionPersonaMappingScopeKey(binding),
		Persona:           binding.PersonaID,
		ProfileVersion:    binding.PersonaVersion,
		SlotID:            binding.SlotID,
		SessionEpoch:      binding.SessionEpoch,
		PersonaGeneration: binding.SlotGeneration,
		SlotGeneration:    binding.SlotGeneration,
		SlotSetGeneration: binding.SlotSetGeneration,
		CredentialChainID: strings.TrimSpace(binding.CredentialChainID),
		ThreadID:          strings.TrimSpace(binding.ClientThreadID),
	}
}

func openAIPersonaMappingExpiry(now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return now.Add(30 * 24 * time.Hour)
}

func openAIPersonaClientThreadID(c *gin.Context, binding SessionPersonaSlotBinding, body []byte) string {
	if value := strings.TrimSpace(binding.ClientThreadID); value != "" {
		return value
	}
	if c != nil && c.Request != nil {
		for _, key := range []string{"x-codex-thread-id", "x-codex-parent-thread-id", "session_id", "conversation_id"} {
			if value := strings.TrimSpace(c.Request.Header.Get(key)); value != "" {
				return value
			}
		}
	}
	if len(body) > 0 {
		for _, path := range []string{"thread_id", "session_id", "conversation_id", "prompt_cache_key"} {
			if value := strings.TrimSpace(gjson.GetBytes(body, path).String()); value != "" {
				return value
			}
		}
	}
	// A root request without an explicit client thread still needs a stable
	// mapping key for retries within the same request scope.
	return "client-thread-" + strings.TrimPrefix(SessionPersonaMappingScopeKey(binding), "pm_")
}

func openAIPersonaClientResponseID(binding SessionPersonaSlotBinding, openCodeID string) string {
	seed := strings.Join([]string{
		"openai-opencode-client-response:v1",
		SessionPersonaMappingScopeKey(binding),
		strings.TrimSpace(openCodeID),
	}, "|")
	digest := sha256.Sum256([]byte(seed))
	return "resp_" + hex.EncodeToString(digest[:16])
}

// EnsureOpenCodeThreadMapping persists the client thread ↔ OpenCode session
// pair and returns a binding carrying the authoritative upstream session ID.
func (s *OpenAIGatewayService) EnsureOpenCodeThreadMapping(ctx context.Context, c *gin.Context, binding SessionPersonaSlotBinding, body []byte) (SessionPersonaSlotBinding, error) {
	if !IsOpenCodePersona(binding) {
		return binding, nil
	}
	binding = binding.NormalizeLifecycle()
	binding.ClientThreadID = openAIPersonaClientThreadID(c, binding, body)
	binding.MappingKey = SessionPersonaMappingScopeKey(binding)
	binding.UpstreamSessionID = EffectiveOpenCodeSessionID(binding)
	store := openAIPersonaIDMappingStore(s)
	if store == nil {
		// Keep service-level unit callers working when they intentionally inject a
		// lightweight AccountRepository; production repositories implement the
		// durable store and therefore never take this compatibility branch.
		return binding, nil
	}
	scope := OpenAIPersonaIDMappingScopeForBinding(c, binding)
	row, err := store.GetOpenAIPersonaIDMappingByClient(ctx, scope, OpenAIPersonaMappingThread, binding.ClientThreadID)
	if err != nil && !errors.Is(err, ErrOpenAIPersonaIDMappingNotFound) {
		return binding, fmt.Errorf("load OpenCode thread mapping: %w", err)
	}
	if row != nil {
		if strings.TrimSpace(row.OpenCodeID) != strings.TrimSpace(binding.UpstreamSessionID) {
			return binding, fmt.Errorf("%w: client thread %q maps to %q, requested %q", ErrOpenAIPersonaIDMappingConflict, binding.ClientThreadID, row.OpenCodeID, binding.UpstreamSessionID)
		}
		binding.UpstreamSessionID = row.OpenCodeID
		return binding, nil
	}
	row = &OpenAIPersonaIDMapping{
		Scope:       scope,
		MappingType: OpenAIPersonaMappingThread,
		ClientID:    binding.ClientThreadID,
		OpenCodeID:  binding.UpstreamSessionID,
		Status:      openAIPersonaMappingStatusActive,
		ExpiresAt:   openAIPersonaMappingExpiry(time.Now().UTC()),
	}
	if _, err := store.UpsertOpenAIPersonaIDMapping(ctx, row); err != nil {
		return binding, fmt.Errorf("persist OpenCode thread mapping: %w", err)
	}
	return binding, nil
}

// RewriteOpenCodeContinuation replaces the downstream client response ID with
// the OpenCode-native response ID in the same complete mapping scope.
func (s *OpenAIGatewayService) RewriteOpenCodeContinuation(ctx context.Context, c *gin.Context, binding SessionPersonaSlotBinding, body []byte) ([]byte, error) {
	if !IsOpenCodePersona(binding) || len(body) == 0 {
		return body, nil
	}
	clientID := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String())
	binding = binding.NormalizeLifecycle()
	binding.ClientThreadID = openAIPersonaClientThreadID(c, binding, body)
	rewritten := body
	if clientID != "" {
		store := openAIPersonaIDMappingStore(s)
		if store == nil {
			return nil, fmt.Errorf("%w: OpenCode continuation mapping store is unavailable", ErrOpenAIPersonaIDMappingUnavailable)
		}
		scope := OpenAIPersonaIDMappingScopeForBinding(c, binding)
		row, err := store.GetOpenAIPersonaIDMappingByClient(ctx, scope, OpenAIPersonaMappingResponse, clientID)
		if err != nil && !errors.Is(err, ErrOpenAIPersonaIDMappingNotFound) {
			return nil, fmt.Errorf("load OpenCode response mapping: %w", err)
		}
		if row == nil || strings.TrimSpace(row.OpenCodeID) == "" {
			return nil, fmt.Errorf("%w: client response %q", ErrOpenAIPersonaIDMappingUnavailable, clientID)
		}
		rewritten, err = sjson.SetBytes(rewritten, "previous_response_id", row.OpenCodeID)
		if err != nil {
			return nil, fmt.Errorf("rewrite OpenCode previous_response_id: %w", err)
		}
	}
	// These markers belong to the Codex client protocol. Once the request is
	// anchored to the OpenCode session mapping, dropping them is safe and avoids
	// feeding an unsupported Codex continuation blob to OpenCode.
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err == nil {
		for _, key := range []string{
			"x-codex-turn-state", "x_codex_turn_state", "x-codex-turn-metadata", "x_codex_turn_metadata",
			"x-codex-parent-thread-id", "x_codex_parent_thread_id", "x-codex-forked-from-thread-id", "x_codex_forked_from_thread_id",
			"parent-thread-id", "parent_thread_id", "forked-from-thread-id", "forked_from_thread_id",
			"parent_turn_id", "root_turn_id", "continuation", "resume", "resume_from",
		} {
			delete(payload, key)
		}
		if rebuilt, marshalErr := json.Marshal(payload); marshalErr == nil {
			rewritten = rebuilt
		}
	}
	return rewritten, nil
}

// ProjectOpenCodeResponseJSON persists and projects the upstream response ID
// back to the client-facing ID. Only response identity fields are rewritten;
// event IDs and output item IDs remain native to their respective protocol.
func (s *OpenAIGatewayService) ProjectOpenCodeResponseJSON(ctx context.Context, c *gin.Context, binding SessionPersonaSlotBinding, payload []byte) ([]byte, string, error) {
	if !IsOpenCodePersona(binding) || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload, extractOpenAIResponseIDFromJSONBytes(payload), nil
	}
	upstreamID := extractOpenAIResponseIDFromJSONBytes(payload)
	if upstreamID == "" {
		if raw, ok := c.Get(openAIPersonaMappingResponseKey); ok {
			if mapping, ok := raw.(*OpenAIPersonaIDMapping); ok && mapping != nil {
				upstreamID = mapping.OpenCodeID
			}
		}
	}
	if upstreamID == "" {
		return payload, "", nil
	}
	binding = binding.NormalizeLifecycle()
	binding.ClientThreadID = openAIPersonaClientThreadID(c, binding, nil)
	clientID := ""
	if raw, ok := c.Get(openAIPersonaMappingResponseKey); ok {
		if mapping, ok := raw.(*OpenAIPersonaIDMapping); ok && mapping != nil && mapping.OpenCodeID == upstreamID {
			clientID = mapping.ClientID
		}
	}
	if clientID == "" {
		clientID = openAIPersonaClientResponseID(binding, upstreamID)
	}
	store := openAIPersonaIDMappingStore(s)
	if store != nil {
		scope := OpenAIPersonaIDMappingScopeForBinding(c, binding)
		row, err := store.GetOpenAIPersonaIDMappingByOpenCode(ctx, scope, OpenAIPersonaMappingResponse, upstreamID)
		if err != nil && !errors.Is(err, ErrOpenAIPersonaIDMappingNotFound) {
			return nil, "", fmt.Errorf("load OpenCode response mapping by upstream ID: %w", err)
		}
		if row != nil {
			clientID = row.ClientID
		} else {
			row = &OpenAIPersonaIDMapping{
				Scope:       scope,
				MappingType: OpenAIPersonaMappingResponse,
				ClientID:    clientID,
				OpenCodeID:  upstreamID,
				Status:      openAIPersonaMappingStatusActive,
				ExpiresAt:   openAIPersonaMappingExpiry(time.Now().UTC()),
			}
			if saved, saveErr := store.UpsertOpenAIPersonaIDMapping(ctx, row); saveErr != nil {
				return nil, "", fmt.Errorf("persist OpenCode response mapping: %w", saveErr)
			} else if saved != nil {
				row = saved
				clientID = row.ClientID
			}
		}
		c.Set(openAIPersonaMappingResponseKey, row)
	}
	rewritten := payload
	var rewriteErr error
	if gjson.GetBytes(rewritten, "response.id").String() == upstreamID {
		if rewritten, rewriteErr = sjson.SetBytes(rewritten, "response.id", clientID); rewriteErr != nil {
			return nil, "", fmt.Errorf("project OpenCode response.id: %w", rewriteErr)
		}
	}
	if gjson.GetBytes(rewritten, "object").String() == "response" && gjson.GetBytes(rewritten, "id").String() == upstreamID {
		if rewritten, rewriteErr = sjson.SetBytes(rewritten, "id", clientID); rewriteErr != nil {
			return nil, "", fmt.Errorf("project OpenCode root response id: %w", rewriteErr)
		}
	}
	return rewritten, clientID, nil
}

// ResolveOpenAIPersonaBindingForClientResponse loads the durable binding used
// by an HTTP/WS continuation before account scheduling chooses a new attempt.
func (s *OpenAIGatewayService) ResolveOpenAIPersonaBindingForClientResponse(ctx context.Context, userID, apiKeyID int64, clientResponseID string, account *Account) (SessionPersonaSlotBinding, bool, error) {
	store := openAIPersonaIDMappingStore(s)
	if store == nil || userID <= 0 || apiKeyID <= 0 || strings.TrimSpace(clientResponseID) == "" {
		return SessionPersonaSlotBinding{}, false, nil
	}
	row, err := store.FindOpenAIPersonaIDMappingByPrincipal(ctx, userID, apiKeyID, OpenAIPersonaMappingResponse, strings.TrimSpace(clientResponseID))
	if err != nil {
		if errors.Is(err, ErrOpenAIPersonaIDMappingNotFound) {
			return SessionPersonaSlotBinding{}, false, nil
		}
		return SessionPersonaSlotBinding{}, false, err
	}
	if row == nil || account == nil || row.Scope.AccountID != account.ID {
		return SessionPersonaSlotBinding{}, false, nil
	}
	persona, ok := NewDefaultSessionPersonaRegistry().Get(string(row.Scope.Persona))
	if !ok {
		return SessionPersonaSlotBinding{}, false, fmt.Errorf("unknown mapped Persona %q", row.Scope.Persona)
	}
	binding := SessionPersonaSlotBinding{
		AccountID:         row.Scope.AccountID,
		SlotID:            row.Scope.SlotID,
		SlotCount:         DefaultSessionPersonaSlotCount,
		ScopeVersion:      SessionPersonaScopeVersionV3,
		MappingVersion:    SessionPersonaScopeVersionV3,
		PersonaID:         row.Scope.Persona,
		PersonaVersion:    persona.EffectiveVersion(),
		CredentialChainID: row.Scope.CredentialChainID,
		State:             SessionPersonaSlotStateActive,
		Enabled:           true,
		Authorized:        true,
		SessionEpoch:      row.Scope.SessionEpoch,
		SlotGeneration:    row.Scope.SlotGeneration,
		SlotSetGeneration: row.Scope.SlotSetGeneration,
		ClientThreadID:    row.Scope.ThreadID,
		ClientResponseID:  row.ClientID,
		MappingKey:        row.Scope.ScopeKey,
		Mapping:           SessionPersonaMappingPersonaV3,
		Persona:           persona,
	}
	binding.UpstreamSessionID = EffectiveOpenCodeSessionID(binding)
	binding, ok = ResolveSessionPersonaBindingForExistingThread(account, binding)
	if !ok {
		return SessionPersonaSlotBinding{}, false, nil
	}
	return binding, true, nil
}
