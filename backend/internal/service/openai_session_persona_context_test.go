package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestClearSessionPersonaBindingPreservesContextAndIsIdempotent(t *testing.T) {
	type markerKey struct{}
	marker := markerKey{}
	base := context.WithValue(context.Background(), marker, "keep-me")
	binding, err := ResolveDefaultSessionPersonaSlot(0)
	if err != nil {
		t.Fatalf("ResolveDefaultSessionPersonaSlot() error = %v", err)
	}
	bound := ContextWithSessionPersonaBinding(base, binding)
	if _, ok := SessionPersonaBindingFromContext(bound); !ok {
		t.Fatal("binding was not attached to context")
	}

	cleared := ClearSessionPersonaBindingFromContext(bound)
	if _, ok := SessionPersonaBindingFromContext(cleared); ok {
		t.Fatal("binding survived context clear")
	}
	if got := cleared.Value(marker); got != "keep-me" {
		t.Fatalf("unrelated context value changed: %#v", got)
	}
	if again := ClearSessionPersonaBindingFromContext(cleared); again != cleared {
		t.Fatal("clearing an already clear context created another wrapper")
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(bound)
	if !ClearSessionPersonaBindingFromGin(c) {
		t.Fatal("Gin clear did not report a binding")
	}
	if _, ok := SessionPersonaBindingFromGin(c); ok {
		t.Fatal("Gin request still exposes cleared binding")
	}
	if ClearSessionPersonaBindingFromGin(c) {
		t.Fatal("Gin clear is not idempotent")
	}
}
func TestClearSessionPersonaBindingFromGinHandlesNilInputs(t *testing.T) {
	if ClearSessionPersonaBindingFromGin(nil) {
		t.Fatal("nil Gin context reported a clear")
	}
	c := &gin.Context{}
	if ClearSessionPersonaBindingFromGin(c) {
		t.Fatal("Gin context without request reported a clear")
	}
	//nolint:staticcheck // 显式覆盖历史调用方传入 nil context 的兼容保护。
	if got := ClearSessionPersonaBindingFromContext(nil); got != nil {
		t.Fatal("nil context was not preserved")
	}
}

func TestSessionPersonaBindingPrefersDynamicExecutionTarget(t *testing.T) {
	legacy, err := ResolveDefaultSessionPersonaSlot(0)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ContextWithSessionPersonaBinding(context.Background(), legacy)
	target := OpenAIExecutionTarget{
		AccountID: 42, AccountPersonaID: 81, PersonaGeneration: 3, SessionEpoch: 7,
		SessionStartedAt: time.Unix(1_700_000_000, 0), DeviceSeed: []byte("0123456789abcdef0123456789abcdef"),
		CredentialChainID: "chain-opencode", ProfileID: SessionPersonaOpenCode,
		ProfileVersion: SessionPersonaOpenCodeVersion, InstallationID: "install-opencode",
		UpstreamSessionID: "session-opencode",
	}
	ctx = ContextWithOpenAIExecutionTarget(ctx, target)

	binding, ok := SessionPersonaBindingFromContext(ctx)
	if !ok || binding.AccountPersonaID != target.AccountPersonaID || binding.PersonaID != target.ProfileID {
		t.Fatalf("dynamic target did not override legacy binding: %#v", binding)
	}
}
