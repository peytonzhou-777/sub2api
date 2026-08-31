package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type openAIPersonaAdmissionTestContextKey struct{}

func newOpenAIPersonaAdmissionTestContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c
}

func openAIPersonaAdmissionTestBinding() service.SessionPersonaSlotBinding {
	return service.SessionPersonaSlotBinding{
		AccountID:         42,
		SlotID:            1,
		SlotCount:         2,
		ScopeVersion:      service.SessionPersonaScopeVersionV3,
		MappingVersion:    service.SessionPersonaScopeVersionV3,
		PersonaID:         service.SessionPersonaOpenCode,
		PersonaVersion:    service.SessionPersonaOpenCodeVersion,
		CredentialChainID: "opencode-chain-1",
		State:             service.SessionPersonaSlotStateDraining,
		Enabled:           true,
		Authorized:        true,
		SessionEpoch:      7,
		SlotGeneration:    4,
		SlotSetGeneration: 9,
		Mapping:           service.SessionPersonaMappingPersonaV3,
	}
}

func TestResolveOpenAIPersonaAdmissionBindingKeepsContinuationBinding(t *testing.T) {
	c := newOpenAIPersonaAdmissionTestContext(t)
	account := &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	base := context.WithValue(context.Background(), openAIPersonaAdmissionTestContextKey{}, "preserved")
	binding := openAIPersonaAdmissionTestBinding()
	bound := service.ContextWithSessionPersonaBinding(base, binding)
	c.Request = c.Request.WithContext(bound)

	gotCtx, gotBinding, ok := resolveOpenAIPersonaAdmissionBinding(bound, c, account)
	if !ok {
		t.Fatal("expected the existing continuation binding to be reused")
	}
	if gotBinding.State != service.SessionPersonaSlotStateDraining {
		t.Fatalf("continuation state = %q, want draining", gotBinding.State)
	}
	if gotBinding.PersonaID != service.SessionPersonaOpenCode || gotBinding.SlotID != 1 {
		t.Fatalf("unexpected continuation binding: %+v", gotBinding)
	}
	if gotBinding.CredentialChainID != "opencode-chain-1" || gotBinding.SlotGeneration != 4 || gotBinding.SlotSetGeneration != 9 {
		t.Fatalf("continuation metadata was not preserved: %+v", gotBinding)
	}
	if got := gotCtx.Value(openAIPersonaAdmissionTestContextKey{}); got != "preserved" {
		t.Fatalf("derived context lost existing value: %v", got)
	}
	attached, attachedOK := service.SessionPersonaBindingFromGin(c)
	if !attachedOK || attached.PersonaID != service.SessionPersonaOpenCode || attached.SlotID != 1 {
		t.Fatalf("Gin request did not retain binding: %+v, ok=%t", attached, attachedOK)
	}
}

func TestResolveOpenAIPersonaAdmissionBindingLeavesLegacyPathUnchanged(t *testing.T) {
	c := newOpenAIPersonaAdmissionTestContext(t)
	account := &service.Account{ID: 42, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	base := context.WithValue(context.Background(), openAIPersonaAdmissionTestContextKey{}, "legacy")

	gotCtx, gotBinding, ok := resolveOpenAIPersonaAdmissionBinding(base, c, account)
	if ok {
		t.Fatalf("unexpected binding without prepared v3 mapping: %+v", gotBinding)
	}
	if gotCtx != base {
		t.Fatal("legacy path should return the original context")
	}
	if _, attached := service.SessionPersonaBindingFromGin(c); attached {
		t.Fatal("legacy path unexpectedly attached a Persona binding")
	}
}

func TestResolveOpenAIPersonaAdmissionBindingRejectsNonOAuthAccount(t *testing.T) {
	c := newOpenAIPersonaAdmissionTestContext(t)
	base := context.Background()
	gotCtx, _, ok := resolveOpenAIPersonaAdmissionBinding(base, c, &service.Account{
		ID:       42,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
	})
	if ok || gotCtx != base {
		t.Fatalf("non-OAuth account changed binding path: ok=%t ctx_changed=%t", ok, gotCtx != base)
	}
}

func TestPopulateOpenAIPersonaAdmissionRequest(t *testing.T) {
	binding := openAIPersonaAdmissionTestBinding()
	req := service.OpenAIAccountAdmissionRequest{AccountID: 42, SlotID: 99}
	populateOpenAIPersonaAdmissionRequest(&req, binding, true)
	if req.Persona != string(service.SessionPersonaOpenCode) || req.SlotID != 1 {
		t.Fatalf("unexpected Persona placement: %+v", req)
	}
	if req.SlotGeneration != 4 || req.SlotSetGeneration != 9 || req.CredentialChainID != "opencode-chain-1" {
		t.Fatalf("unexpected generation/credential metadata: %+v", req)
	}

	legacy := service.OpenAIAccountAdmissionRequest{AccountID: 42, SlotID: 99}
	populateOpenAIPersonaAdmissionRequest(&legacy, binding, false)
	if legacy.Persona != "" || legacy.SlotID != 99 || legacy.SlotGeneration != 0 || legacy.CredentialChainID != "" {
		t.Fatalf("legacy request was modified: %+v", legacy)
	}
}

func TestApplyOpenAIPersonaAdmissionPolicy(t *testing.T) {
	cfg := service.DefaultOpenAIAccountAdmissionConfig()
	cfg.RequestsPerMinute = 10
	cfg.TokensPerMinute = 1000
	cfg.PersonaPolicies = map[string]service.OpenAIPersonaAdmissionPolicy{
		"opencode": {
			MaxConcurrency:          3,
			RequestsPerMinute:       20,
			TokensPerMinute:         2000,
			MaxQueueDepthPerAccount: 7,
		},
	}

	account := &service.Account{Concurrency: 12}
	got, accountConcurrency, personaConcurrency := applyOpenAIPersonaAdmissionPolicy(cfg, account, openAIPersonaAdmissionTestBinding(), 12)
	if accountConcurrency != 12 || personaConcurrency != 3 {
		t.Fatalf("effective concurrency = account %d persona %d, want 12/3", accountConcurrency, personaConcurrency)
	}
	if got.RequestsPerMinute != 20 || got.TokensPerMinute != 2000 || got.MaxQueueDepthPerAccount != 7 {
		t.Fatalf("Persona rate/queue policy not applied: %+v", got)
	}
}
