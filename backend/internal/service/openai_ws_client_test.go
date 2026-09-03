package service

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCoderOpenAIWSClientDialer_ProxyHTTPClientReuse(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	c1, err := impl.proxyHTTPClient("http://127.0.0.1:8080")
	require.NoError(t, err)
	c2, err := impl.proxyHTTPClient("http://127.0.0.1:8080")
	require.NoError(t, err)
	require.Same(t, c1, c2, "同一代理地址应复用同一个 HTTP 客户端")

	c3, err := impl.proxyHTTPClient("http://127.0.0.1:8081")
	require.NoError(t, err)
	require.NotSame(t, c1, c3, "不同代理地址应分离客户端")
}

func TestCoderOpenAIWSClientDialer_CPAClientIsScopedByPersonaAndCredentialChain(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	base := OpenAITransportScope{
		AccountID:         42,
		AccountPersonaID:  4201,
		ProfileID:         SessionPersonaCodexCLIStrict,
		ProfileVersion:    SessionPersonaCodexCLIStrictVersion,
		PersonaGeneration: 2,
		SessionEpoch:      3,
		CredentialChainID: "codex-chain-1",
		InstallationID:    "install-1",
	}

	c1, err := impl.cpaHTTPClient(base, "http://proxy-a.example:8080")
	require.NoError(t, err)
	c2, err := impl.cpaHTTPClient(base, "http://proxy-a.example:8080")
	require.NoError(t, err)
	require.Same(t, c1, c2, "同一完整作用域应复用 CPA WS HTTP 客户端")

	otherPersona := base
	otherPersona.AccountPersonaID++
	c3, err := impl.cpaHTTPClient(otherPersona, "http://proxy-a.example:8080")
	require.NoError(t, err)
	require.NotSame(t, c1, c3, "不同 AccountPersona 不得复用 CPA WS HTTP 客户端")

	otherChain := base
	otherChain.CredentialChainID = "codex-chain-2"
	c4, err := impl.cpaHTTPClient(otherChain, "http://proxy-a.example:8080")
	require.NoError(t, err)
	require.NotSame(t, c1, c4, "不同 credential chain 不得复用 CPA WS HTTP 客户端")

	transport, ok := c1.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.DialTLSContext, "CPA WS 客户端必须使用 Chrome TLS 拨号器")
	snapshot := impl.SnapshotTransportMetrics()
	require.Equal(t, int64(1), snapshot.CPAClientCacheHits)
	require.Equal(t, int64(3), snapshot.CPAClientCacheMisses)
}

func TestCoderOpenAIWSClientDialer_InvalidationOnlyRemovesMatchingCredential(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)
	target := OpenAITransportScope{
		AccountID: 42, AccountPersonaID: 4202, ProfileID: SessionPersonaOpenCode, ProfileVersion: "1.18.23",
		PersonaGeneration: 2, SessionEpoch: 3,
		CredentialChainID: "chain-target", InstallationID: "install-target",
	}
	other := target
	other.CredentialChainID = "chain-other"
	other.InstallationID = "install-other"

	_, err := impl.cpaHTTPClient(target, "http://proxy-a.example:8080")
	require.NoError(t, err)
	_, err = impl.cpaHTTPClient(other, "http://proxy-a.example:8080")
	require.NoError(t, err)
	impl.invalidateAccountPersonaCredentialTransport(target.AccountPersonaID, target.CredentialChainID)

	impl.cpaMu.Lock()
	require.Len(t, impl.cpaClients, 1)
	for _, entry := range impl.cpaClients {
		require.Equal(t, other.CredentialChainID, entry.transportScope.CredentialChainID)
	}
	impl.cpaMu.Unlock()
}

func TestCoderOpenAIWSClientDialer_CPAClientRejectsIncompleteScope(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	_, err := impl.cpaHTTPClient(OpenAITransportScope{AccountID: 42}, "")
	require.Error(t, err)
}

func TestCoderOpenAIWSClientDialer_ProxyHTTPClientInvalidURL(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	_, err := impl.proxyHTTPClient("://bad")
	require.Error(t, err)
}

func TestCoderOpenAIWSClientDialer_TransportMetricsSnapshot(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	_, err := impl.proxyHTTPClient("http://127.0.0.1:18080")
	require.NoError(t, err)
	_, err = impl.proxyHTTPClient("http://127.0.0.1:18080")
	require.NoError(t, err)
	_, err = impl.proxyHTTPClient("http://127.0.0.1:18081")
	require.NoError(t, err)

	snapshot := impl.SnapshotTransportMetrics()
	require.Equal(t, int64(1), snapshot.ProxyClientCacheHits)
	require.Equal(t, int64(2), snapshot.ProxyClientCacheMisses)
	require.InDelta(t, 1.0/3.0, snapshot.TransportReuseRatio, 0.0001)
}

func TestCoderOpenAIWSClientDialer_ProxyClientCacheCapacity(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	total := openAIWSProxyClientCacheMaxEntries + 32
	for i := 0; i < total; i++ {
		_, err := impl.proxyHTTPClient(fmt.Sprintf("http://127.0.0.1:%d", 20000+i))
		require.NoError(t, err)
	}

	impl.proxyMu.Lock()
	cacheSize := len(impl.proxyClients)
	impl.proxyMu.Unlock()

	require.LessOrEqual(t, cacheSize, openAIWSProxyClientCacheMaxEntries, "代理客户端缓存应受容量上限约束")
}

func TestCoderOpenAIWSClientDialer_ProxyClientCacheIdleTTL(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	oldProxy := "http://127.0.0.1:28080"
	_, err := impl.proxyHTTPClient(oldProxy)
	require.NoError(t, err)

	impl.proxyMu.Lock()
	oldEntry := impl.proxyClients[oldProxy]
	require.NotNil(t, oldEntry)
	oldEntry.lastUsedUnixNano = time.Now().Add(-openAIWSProxyClientCacheIdleTTL - time.Minute).UnixNano()
	impl.proxyMu.Unlock()

	// 触发一次新的代理获取，驱动 TTL 清理。
	_, err = impl.proxyHTTPClient("http://127.0.0.1:28081")
	require.NoError(t, err)

	impl.proxyMu.Lock()
	_, exists := impl.proxyClients[oldProxy]
	impl.proxyMu.Unlock()

	require.False(t, exists, "超过空闲 TTL 的代理客户端应被回收")
}

func TestCoderOpenAIWSClientDialer_ProxyTransportTLSHandshakeTimeout(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	client, err := impl.proxyHTTPClient("http://127.0.0.1:38080")
	require.NoError(t, err)
	require.NotNil(t, client)

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport)
	require.Equal(t, 10*time.Second, transport.TLSHandshakeTimeout)
}

func TestCoderOpenAIWSClientConn_DoesNotSupportIdlePingWithoutReader(t *testing.T) {
	require.False(t, (&coderOpenAIWSClientConn{}).SupportsIdlePingWithoutReader())
}
