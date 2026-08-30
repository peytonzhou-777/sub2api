package tlsfingerprint

import (
	"crypto/tls"
	"testing"
)

func TestCPAChromeDialerUsesExplicitChromeALPNAndSessionCache(t *testing.T) {
	cache := NewCPAChromeSessionCache()
	if cache == nil {
		t.Fatal("CPA session cache is nil")
	}
	dialer := NewCPAChromeDialer(nil, cache, true)
	if dialer == nil || !dialer.requireH2 || dialer.sessionCache != cache {
		t.Fatal("CPA Chrome dialer was not initialized with strict H2 settings")
	}

	config := cloneUTLSConfig(&tls.Config{ServerName: "chatgpt.com"}, "ignored.example", cache)
	if config.ServerName != "chatgpt.com" {
		t.Fatalf("unexpected server name: %q", config.ServerName)
	}
	if len(config.NextProtos) != 2 || config.NextProtos[0] != "h2" || config.NextProtos[1] != "http/1.1" {
		t.Fatalf("unexpected ALPN list: %#v", config.NextProtos)
	}
	if config.MinVersion != tls.VersionTLS12 || config.MaxVersion != tls.VersionTLS13 {
		t.Fatalf("unexpected TLS version bounds: %d-%d", config.MinVersion, config.MaxVersion)
	}
	if config.ClientSessionCache != cache {
		t.Fatal("session cache was not scoped to the CPA transport")
	}
}

func TestCPAChromeWSH1DialerPinsHTTP11ALPN(t *testing.T) {
	cache := NewCPAChromeSessionCache()
	dialer := NewCPAChromeWSH1Dialer(nil, cache)
	if dialer == nil || dialer.requireH2 || dialer.sessionCache != cache {
		t.Fatal("CPA Chrome WS dialer was not initialized for HTTP/1.1")
	}
	config := cloneUTLSConfig(nil, "chatgpt.com", cache)
	if len(dialer.nextProtos) != 1 || dialer.nextProtos[0] != "http/1.1" {
		t.Fatalf("unexpected WS ALPN override: %#v", dialer.nextProtos)
	}
	if len(config.NextProtos) != 2 || config.NextProtos[0] != "h2" || config.NextProtos[1] != "http/1.1" {
		t.Fatalf("base Chrome ALPN should remain unchanged: %#v", config.NextProtos)
	}
}
