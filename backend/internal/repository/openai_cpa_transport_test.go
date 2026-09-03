package repository

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"golang.org/x/net/http2"
)

func TestDoWithTLSRoutesCompleteAccountPersonaScopeToCPAManager(t *testing.T) {
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{Enabled: true},
		},
	}
	svc := &httpUpstreamService{cfg: cfg, clients: make(map[string]*upstreamClientEntry)}
	target := service.OpenAIExecutionTarget{
		AccountID:         42,
		AccountPersonaID:  4201,
		ProfileID:         service.SessionPersonaOpenCode,
		ProfileVersion:    "1.18.23",
		CredentialChainID: "chain-opencode",
		InstallationID:    "install-opencode",
		SessionEpoch:      1,
		SessionStartedAt:  time.Unix(1_700_000_000, 0),
		DeviceSeed:        []byte("0123456789abcdef0123456789abcdef"),
		PersonaGeneration: 1,
		UpstreamSessionID: "session-opencode",
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.openai.com/v1/responses", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	ctx := service.WithHTTPUpstreamProfile(req.Context(), service.HTTPUpstreamProfileOpenAI)
	ctx = service.ContextWithOpenAIExecutionTarget(ctx, target)
	req = req.WithContext(ctx)

	manager := svc.ensureOpenAICPAManager()
	settings := svc.applyProfilePoolSettings(svc.resolvePoolSettings(svc.getIsolationMode(), 1), service.HTTPUpstreamProfileOpenAI)
	poolKey := buildPoolKey(settings, upstreamProtocolModeOpenAICPAH2) + "|profile:" + tlsfingerprint.CPAChromeProfileVersion
	fingerprint := openAITransportScopeFingerprint(service.OpenAITransportScope{
		AccountID: target.AccountID, AccountPersonaID: target.AccountPersonaID,
		ProfileID: target.ProfileID, ProfileVersion: target.ProfileVersion,
		SessionEpoch: target.SessionEpoch, PersonaGeneration: target.PersonaGeneration,
		CredentialChainID: target.CredentialChainID, InstallationID: target.InstallationID,
	}, directProxyKey, poolKey)
	routed := false
	manager.clients["cpa:"+fingerprint] = &upstreamClientEntry{
		client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			routed = true
			return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody}, nil
		})},
		proxyKey:                directProxyKey,
		poolKey:                 poolKey,
		protocolMode:            upstreamProtocolModeOpenAICPAH2,
		transportManagerID:      manager.id,
		transportGeneration:     manager.generation,
		scopeFingerprint:        fingerprint,
		transportProfileVersion: tlsfingerprint.CPAChromeProfileVersion,
	}

	resp, err := svc.DoWithTLS(req, "", target.AccountID, 1, &tlsfingerprint.Profile{Name: "legacy-input-is-ignored"})
	if err != nil {
		t.Fatalf("CPA-routed request failed: %v", err)
	}
	if resp == nil || !routed {
		t.Fatal("complete Persona scope did not use the CPA manager")
	}
	if len(svc.clients) != 0 {
		t.Fatal("CPA-routed request populated the legacy account/proxy client cache")
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}
}

func TestOpenAICPATransportIsolatedByScopeAndManagerGeneration(t *testing.T) {
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
				Enabled:                   true,
				AllowProxyFallbackToHTTP1: true,
				FallbackErrorThreshold:    2,
				FallbackWindowSeconds:     60,
				FallbackTTLSeconds:        600,
			},
			MaxUpstreamClients: 10,
		},
	}
	svc := &httpUpstreamService{cfg: cfg, clients: make(map[string]*upstreamClientEntry)}

	scope0 := service.OpenAITransportScope{
		AccountID:         42,
		AccountPersonaID:  4201,
		ProfileID:         service.SessionPersonaCodexCLIStrict,
		ProfileVersion:    "0.149.0",
		PersonaGeneration: 1,
		SessionEpoch:      1,
		CredentialChainID: "chain-codex",
		InstallationID:    "install-codex",
	}
	scope1 := scope0
	scope1.AccountPersonaID++
	scope1.ProfileID = service.SessionPersonaOpenCode
	scope1.ProfileVersion = "1.18.23"
	scope1.CredentialChainID = "chain-opencode"
	scope1.InstallationID = "install-opencode"

	manager := svc.ensureOpenAICPAManager()
	poolKey := "test-pool|profile:" + tlsfingerprint.CPAChromeProfileVersion
	fingerprint0 := openAITransportScopeFingerprint(scope0, "direct", poolKey)
	fingerprint1 := openAITransportScopeFingerprint(scope1, "direct", poolKey)
	entry0, err := svc.acquireOpenAICPAClient(manager, fingerprint0, "direct", nil, poolKey, poolSettings{}, 42, 1, scope0)
	if err != nil {
		t.Fatalf("slot 0 acquire failed: %v", err)
	}
	entry0Again, err := svc.acquireOpenAICPAClient(manager, fingerprint0, "direct", nil, poolKey, poolSettings{}, 42, 1, scope0)
	if err != nil {
		t.Fatalf("slot 0 reuse failed: %v", err)
	}
	if entry0Again != entry0 {
		t.Fatal("same complete scope did not reuse its CPA transport")
	}
	transport, ok := entry0.client.Transport.(*http2.Transport)
	if !ok || transport.DialTLSContext == nil || transport.ReadIdleTimeout != openAIHTTP2ReadIdleTimeout || transport.PingTimeout != openAIHTTP2PingTimeout {
		t.Fatalf("CPA transport is not explicit HTTP/2 with health checks: %#v", entry0.client.Transport)
	}
	entry1, err := svc.acquireOpenAICPAClient(manager, fingerprint1, "direct", nil, poolKey, poolSettings{}, 42, 1, scope1)
	if err != nil {
		t.Fatalf("slot 1 acquire failed: %v", err)
	}
	if entry1 == entry0 || len(manager.clients) != 2 {
		t.Fatal("different Persona/slot scopes shared a CPA transport")
	}

	oldGeneration := manager.generation
	cfg.Gateway.OpenAIHTTP2.FallbackTTLSeconds = 900
	newManager := svc.ensureOpenAICPAManager()
	if newManager == manager || !manager.draining.Load() || newManager.generation <= oldGeneration {
		t.Fatal("CPA manager configuration change did not create a new generation")
	}
	if len(manager.clients) != 0 {
		t.Fatal("draining CPA manager retained entries for new registration")
	}
	entry0New, err := svc.acquireOpenAICPAClient(newManager, fingerprint0, "direct", nil, poolKey, poolSettings{}, 42, 1, scope0)
	if err != nil {
		t.Fatalf("new manager acquire failed: %v", err)
	}
	if entry0New == entry0 || entry0New.transportGeneration == entry0.transportGeneration {
		t.Fatal("new CPA manager reused an old-generation entry")
	}
	if entry0New.transportProfileVersion != tlsfingerprint.CPAChromeProfileVersion {
		t.Fatalf("unexpected CPA profile version: %q", entry0New.transportProfileVersion)
	}
}

func TestOpenAICPATransportInvalidationOnlyRemovesMatchingAccountPersonaCredential(t *testing.T) {
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			OpenAIHTTP2:        config.GatewayOpenAIHTTP2Config{Enabled: true},
			MaxUpstreamClients: 10,
		},
	}
	svc := &httpUpstreamService{cfg: cfg, clients: make(map[string]*upstreamClientEntry)}
	manager := svc.ensureOpenAICPAManager()
	poolKey := "test-pool|profile:" + tlsfingerprint.CPAChromeProfileVersion
	target := service.OpenAITransportScope{
		AccountID: 42, AccountPersonaID: 4201, ProfileID: service.SessionPersonaOpenCode, ProfileVersion: "1.18.23",
		PersonaGeneration: 1, SessionEpoch: 1,
		CredentialChainID: "chain-target", InstallationID: "install-target",
	}
	other := target
	other.CredentialChainID = "chain-other"
	other.InstallationID = "install-other"

	for _, scope := range []service.OpenAITransportScope{target, other} {
		fingerprint := openAITransportScopeFingerprint(scope, directProxyKey, poolKey)
		_, err := svc.acquireOpenAICPAClient(manager, fingerprint, directProxyKey, nil, poolKey, poolSettings{}, 42, 1, scope)
		if err != nil {
			t.Fatalf("acquire CPA transport: %v", err)
		}
	}

	svc.InvalidateOpenAIAccountPersonaCredentialTransport(target.AccountID, target.AccountPersonaID, target.CredentialChainID)
	if len(manager.clients) != 1 {
		t.Fatalf("expected only the non-matching credential transport to remain, got %d", len(manager.clients))
	}
	for _, entry := range manager.clients {
		if entry.openAITransportScope.CredentialChainID != other.CredentialChainID {
			t.Fatalf("unexpected credential transport retained: %q", entry.openAITransportScope.CredentialChainID)
		}
	}
}

func TestOpenAICPATransportDynamicPersonaSessionInvalidation(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{Enabled: true}, MaxUpstreamClients: 10,
	}}
	svc := &httpUpstreamService{cfg: cfg, clients: make(map[string]*upstreamClientEntry)}
	manager := svc.ensureOpenAICPAManager()
	poolKey := "dynamic-test|profile:" + tlsfingerprint.CPAChromeProfileVersion
	target := service.OpenAITransportScope{
		AccountID: 42, AccountPersonaID: 1001, ProfileID: service.SessionPersonaOpenCode,
		ProfileVersion: "1.18.23", PersonaGeneration: 2, SessionEpoch: 5,
		CredentialChainID: "chain", InstallationID: "install", ProxyRevision: 3,
	}
	otherEpoch := target
	otherEpoch.SessionEpoch++
	otherPersona := target
	otherPersona.AccountPersonaID++
	for _, scope := range []service.OpenAITransportScope{target, otherEpoch, otherPersona} {
		fingerprint := openAITransportScopeFingerprint(scope, directProxyKey, poolKey)
		_, err := svc.acquireOpenAICPAClient(manager, fingerprint, directProxyKey, nil, poolKey, poolSettings{}, 42, 1, scope)
		if err != nil {
			t.Fatalf("acquire dynamic CPA transport: %v", err)
		}
	}
	svc.InvalidateOpenAIAccountPersonaSessionTransport(target.AccountID, target.AccountPersonaID, target.SessionEpoch)
	if len(manager.clients) != 2 {
		t.Fatalf("expected only one dynamic epoch to be removed, got %d entries", len(manager.clients))
	}
	for _, entry := range manager.clients {
		if entry.openAITransportScope.MatchesAccountPersonaSession(target.AccountPersonaID, target.SessionEpoch) {
			t.Fatal("invalidated dynamic Persona epoch remained registered")
		}
	}
}
