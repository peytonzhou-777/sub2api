package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"golang.org/x/net/http2"
)

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
		Persona:           service.SessionPersonaCodexCLIStrict,
		PersonaVersion:    "0.149.0",
		SlotID:            0,
		SessionEpoch:      1,
		SlotGeneration:    1,
		SlotSetGeneration: 1,
		CredentialChainID: "chain-codex",
		InstallationID:    "install-codex",
	}
	scope1 := scope0
	scope1.Persona = service.SessionPersonaOpenCode
	scope1.PersonaVersion = "1.18.23"
	scope1.SlotID = 1
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
