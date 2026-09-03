package service

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type inMemoryOpenAIPersonaIDMappingStore struct {
	rows []*OpenAIPersonaIDMapping
}

type openAIPersonaMappingAccountRepo struct {
	AccountRepository
	OpenAIAccountPersonaRepository
	persona OpenAIAccountPersona
	session OpenAIAccountPersonaSession
}

func (r *openAIPersonaMappingAccountRepo) GetAccountPersona(_ context.Context, accountID, personaID int64) (*OpenAIAccountPersona, error) {
	if r.persona.AccountID != accountID || r.persona.ID != personaID {
		return nil, ErrOpenAIAccountPersonaNotFound
	}
	persona := r.persona
	return &persona, nil
}

func (r *openAIPersonaMappingAccountRepo) GetAccountPersonaSession(_ context.Context, accountID, personaID, epoch int64, _ time.Time) (*OpenAIAccountPersonaSession, error) {
	if r.persona.AccountID != accountID || r.session.AccountPersonaID != personaID || r.session.SessionEpoch != epoch {
		return nil, ErrOpenAIAccountPersonaSessionNotFound
	}
	session := r.session
	return &session, nil
}

func (s *inMemoryOpenAIPersonaIDMappingStore) find(scope OpenAIPersonaIDMappingScope, mappingType OpenAIPersonaIDMappingType, clientID, openCodeID string) *OpenAIPersonaIDMapping {
	for _, row := range s.rows {
		if row == nil || row.MappingType != mappingType || row.Scope.ScopeKey != scope.ScopeKey {
			continue
		}
		if clientID != "" && row.ClientID == clientID {
			return row
		}
		if openCodeID != "" && row.OpenCodeID == openCodeID {
			return row
		}
	}
	return nil
}

func (s *inMemoryOpenAIPersonaIDMappingStore) GetOpenAIPersonaIDMappingByClient(_ context.Context, scope OpenAIPersonaIDMappingScope, mappingType OpenAIPersonaIDMappingType, clientID string) (*OpenAIPersonaIDMapping, error) {
	if row := s.find(scope, mappingType, clientID, ""); row != nil {
		return row, nil
	}
	return nil, ErrOpenAIPersonaIDMappingNotFound
}

func (s *inMemoryOpenAIPersonaIDMappingStore) GetOpenAIPersonaIDMappingByOpenCode(_ context.Context, scope OpenAIPersonaIDMappingScope, mappingType OpenAIPersonaIDMappingType, openCodeID string) (*OpenAIPersonaIDMapping, error) {
	if row := s.find(scope, mappingType, "", openCodeID); row != nil {
		return row, nil
	}
	return nil, ErrOpenAIPersonaIDMappingNotFound
}

func (s *inMemoryOpenAIPersonaIDMappingStore) FindOpenAIPersonaIDMappingByPrincipal(_ context.Context, userID, apiKeyID int64, mappingType OpenAIPersonaIDMappingType, clientID string) (*OpenAIPersonaIDMapping, error) {
	for _, row := range s.rows {
		if row != nil && row.Scope.UserID == userID && row.Scope.APIKeyID == apiKeyID && row.MappingType == mappingType && row.ClientID == clientID {
			return row, nil
		}
	}
	return nil, ErrOpenAIPersonaIDMappingNotFound
}

func (s *inMemoryOpenAIPersonaIDMappingStore) UpsertOpenAIPersonaIDMapping(_ context.Context, mapping *OpenAIPersonaIDMapping) (*OpenAIPersonaIDMapping, error) {
	if existing := s.find(mapping.Scope, mapping.MappingType, mapping.ClientID, ""); existing != nil {
		if existing.OpenCodeID != mapping.OpenCodeID {
			return nil, ErrOpenAIPersonaIDMappingConflict
		}
		return existing, nil
	}
	if existing := s.find(mapping.Scope, mapping.MappingType, "", mapping.OpenCodeID); existing != nil {
		return nil, ErrOpenAIPersonaIDMappingConflict
	}
	copy := *mapping
	copy.ID = int64(len(s.rows) + 1)
	copy.CreatedAt = time.Now().UTC()
	copy.UpdatedAt = copy.CreatedAt
	copy.LastSeenAt = copy.CreatedAt
	s.rows = append(s.rows, &copy)
	return &copy, nil
}

func testOpenCodeBinding() SessionPersonaSlotBinding {
	persona, _ := NewDefaultSessionPersonaRegistry().Get(string(SessionPersonaOpenCode))
	return SessionPersonaSlotBinding{
		AccountID:         42,
		AccountPersonaID:  101,
		SlotID:            1,
		SlotCount:         2,
		ScopeVersion:      SessionPersonaScopeVersionV3,
		MappingVersion:    SessionPersonaScopeVersionV3,
		PersonaID:         SessionPersonaOpenCode,
		PersonaVersion:    persona.EffectiveVersion(),
		CredentialChainID: "chain-opencode-a",
		State:             SessionPersonaSlotStateActive,
		Enabled:           true,
		Authorized:        true,
		SessionEpoch:      7,
		SlotGeneration:    2,
		SlotSetGeneration: 3,
		ClientThreadID:    "thread-client-1",
		Mapping:           SessionPersonaMappingPersonaV3,
		Persona:           persona,
	}
}

func testOpenCodeGinContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{}`))
	SetOpenAIHTTPResponseOwner(c, 9, 10)
	return c
}

func TestSessionPersonaMappingScopeIncludesCredentialChain(t *testing.T) {
	first := testOpenCodeBinding()
	second := first
	second.CredentialChainID = "chain-opencode-b"
	require.NotEqual(t, SessionPersonaMappingScopeKey(first), SessionPersonaMappingScopeKey(second))
}

func TestSessionPersonaMappingScopeSeparatesDynamicAccountPersonas(t *testing.T) {
	first := testOpenCodeBinding()
	first.AccountPersonaID = 101
	first.SlotID = 0
	first.SlotCount = 1
	second := first
	second.AccountPersonaID = 102
	require.NotEqual(t, SessionPersonaMappingScopeKey(first), SessionPersonaMappingScopeKey(second))

	scope := OpenAIPersonaIDMappingScopeForBinding(nil, first)
	require.Equal(t, first.AccountPersonaID, scope.AccountPersonaID)
	require.Equal(t, first.SlotGeneration, scope.PersonaGeneration)
	require.Equal(t, first.PersonaVersion, scope.ProfileVersion)
}

func TestOpenAIPersonaIDMappingRoundTrip(t *testing.T) {
	store := &inMemoryOpenAIPersonaIDMappingStore{}
	svc := &OpenAIGatewayService{personaIDMappingStore: store}
	c := testOpenCodeGinContext()
	binding := testOpenCodeBinding()

	bound, err := svc.EnsureOpenCodeThreadMapping(context.Background(), c, binding, []byte(`{"model":"gpt-5","input":[]}`))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(bound.UpstreamSessionID, "oc_"))
	require.Len(t, store.rows, 1)

	clientBody, err := svc.RewriteOpenCodeContinuation(context.Background(), c, bound, []byte(`{"previous_response_id":"resp_client"}`))
	require.Error(t, err, "unknown response IDs must fail closed")
	require.Nil(t, clientBody)

	projected, clientID, err := svc.ProjectOpenCodeResponseJSON(context.Background(), c, bound, []byte(`{"type":"response.completed","response":{"id":"resp_upstream"}}`))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(clientID, "resp_"))
	require.Equal(t, clientID, gjsonGetString(projected, "response.id"))

	rewritten, err := svc.RewriteOpenCodeContinuation(context.Background(), c, bound, []byte(`{"previous_response_id":"`+clientID+`"}`))
	require.NoError(t, err)
	require.Equal(t, "resp_upstream", gjsonGetString(rewritten, "previous_response_id"))
	projectedBody, err := PrepareOpenCodeOutboundBodyWithMappedContinuation(rewritten, SessionPersonaTransportHTTP, false)
	require.NoError(t, err)
	require.Equal(t, "resp_upstream", gjsonGetString(projectedBody, "previous_response_id"))
	require.Equal(t, true, gjson.GetBytes(projectedBody, "stream").Bool())
}

func TestProjectOpenAICompatPersonaPayloadUsesDynamicTargetScope(t *testing.T) {
	store := &inMemoryOpenAIPersonaIDMappingStore{}
	svc := &OpenAIGatewayService{personaIDMappingStore: store}
	c := testOpenCodeGinContext()
	target := OpenAIExecutionTarget{
		AccountID: 42, AccountPersonaID: 77, PersonaGeneration: 2, SessionEpoch: 5,
		SessionStartedAt: time.Unix(1_700_000_000, 0), DeviceSeed: []byte("0123456789abcdef0123456789abcdef"),
		CredentialChainID: "chain-dynamic", ProfileID: SessionPersonaOpenCode,
		ProfileVersion: SessionPersonaOpenCodeVersion, InstallationID: "install-dynamic",
		UpstreamSessionID: "session-dynamic",
	}
	c.Request = c.Request.WithContext(ContextWithOpenAIExecutionTarget(c.Request.Context(), target))

	projected, err := svc.projectOpenAICompatPersonaPayload(c, []byte(`{"type":"response.completed","response":{"id":"resp_native"}}`))
	require.NoError(t, err)
	require.NotEqual(t, "resp_native", gjson.GetBytes(projected, "response.id").String())
	require.Len(t, store.rows, 1)
	require.Equal(t, target.AccountPersonaID, store.rows[0].Scope.AccountPersonaID)
}

func TestOpenAIPersonaBindingResolvesFromClientResponse(t *testing.T) {
	store := &inMemoryOpenAIPersonaIDMappingStore{}
	target := OpenAIExecutionTarget{
		AccountID: 42, AccountPersonaID: 77, PersonaGeneration: 2, SessionEpoch: 5,
		SessionStartedAt: time.Unix(1_700_000_000, 0), DeviceSeed: []byte("0123456789abcdef0123456789abcdef"),
		CredentialChainID: "chain-dynamic", ProfileID: SessionPersonaOpenCode,
		ProfileVersion: SessionPersonaOpenCodeVersion, InstallationID: "install-dynamic",
		UpstreamSessionID: "session-dynamic",
	}
	binding, bindingOK := SessionPersonaBindingFromExecutionTarget(target)
	require.True(t, bindingOK)
	repo := &openAIPersonaMappingAccountRepo{
		persona: OpenAIAccountPersona{
			ID: target.AccountPersonaID, AccountID: target.AccountID, ProfileID: target.ProfileID,
			ProfileVersion: target.ProfileVersion, PersonaGeneration: target.PersonaGeneration,
			CurrentCredentialChainID: target.CredentialChainID, CurrentSessionEpoch: target.SessionEpoch,
			InstallationID: target.InstallationID, DeviceSeed: append([]byte(nil), target.DeviceSeed...),
			State: OpenAIAccountPersonaStateActive, Enabled: true,
		},
		session: OpenAIAccountPersonaSession{
			AccountPersonaID: target.AccountPersonaID, SessionEpoch: target.SessionEpoch,
			UpstreamSessionID: target.UpstreamSessionID, State: OpenAIPersonaSessionCurrent,
			PersonaGeneration: target.PersonaGeneration, CredentialChainID: target.CredentialChainID,
			ProfileID: target.ProfileID, ProfileVersion: target.ProfileVersion, InstallationID: target.InstallationID,
			StartedAt: target.SessionStartedAt,
		},
	}
	repo.OpenAIAccountPersonaRepository = repo
	svc := &OpenAIGatewayService{personaIDMappingStore: store, accountRepo: repo}
	c := testOpenCodeGinContext()
	bound, err := svc.EnsureOpenCodeThreadMapping(context.Background(), c, binding, nil)
	require.NoError(t, err)
	_, clientID, err := svc.ProjectOpenCodeResponseJSON(context.Background(), c, bound, []byte(`{"id":"resp_upstream","object":"response"}`))
	require.NoError(t, err)
	resolved, ok, err := svc.ResolveOpenAIPersonaBindingForClientResponse(context.Background(), 9, 10, clientID, &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, SessionPersonaOpenCode, resolved.PersonaID)
	require.Equal(t, target.AccountPersonaID, resolved.AccountPersonaID)
	require.Equal(t, target.CredentialChainID, resolved.CredentialChainID)
	require.Equal(t, target.SessionEpoch, resolved.SessionEpoch)
}

func gjsonGetString(body []byte, path string) string {
	return gjson.GetBytes(body, path).String()
}
