package repository

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

func TestCPAChromeHTTP2DirectAndProxyRoutes(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Proto", r.Proto)
		w.WriteHeader(http.StatusNoContent)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	for _, tc := range []struct {
		name      string
		proxyURL  string
		wantProxy bool
	}{
		{name: "direct"},
		{name: "http_connect", wantProxy: true},
		{name: "socks5h", wantProxy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var proxyCalls *atomic.Int64
			proxyURL := tc.proxyURL
			if tc.wantProxy && strings.HasPrefix(tc.name, "http_") {
				proxyURL, proxyCalls = startTestHTTPConnectProxy(t)
			} else if tc.wantProxy {
				proxyURL, proxyCalls = startTestSOCKS5Proxy(t)
			}

			var parsedProxy *url.URL
			if proxyURL != "" {
				var err error
				parsedProxy, err = url.Parse(proxyURL)
				require.NoError(t, err)
			}
			dialer := tlsfingerprint.NewCPAChromeDialer(parsedProxy, tlsfingerprint.NewCPAChromeSessionCache(), true)
			transport := &http2.Transport{
				DialTLSContext: dialer.DialTLSContext,
				// The test server uses an ephemeral self-signed certificate. Production
				// never sets this flag; the CPA dialer still keeps target ALPN strict.
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test-only certificate
			}
			client := &http.Client{Transport: transport}
			resp, err := client.Get(server.URL)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, 2, resp.ProtoMajor)
			require.Equal(t, "HTTP/2.0", resp.Header.Get("X-Test-Proto"))
			if proxyCalls != nil {
				require.Equal(t, int64(1), proxyCalls.Load())
			}
		})
	}
}

func TestCPAChromeHTTPConnectProxySendsAuthorizationOnlyToProxy(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Errorf("proxy authorization leaked to target: %q", r.Header.Get("Proxy-Authorization"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	proxyURL, proxyCalls, authSeen := startTestHTTPConnectProxyWithAuth(t)
	parsedProxy, err := url.Parse(proxyURL)
	require.NoError(t, err)
	parsedProxy.User = url.UserPassword("user", "pass")
	dialer := tlsfingerprint.NewCPAChromeDialer(parsedProxy, tlsfingerprint.NewCPAChromeSessionCache(), true)
	transport := &http2.Transport{
		DialTLSContext:  dialer.DialTLSContext,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test-only certificate
	}
	client := &http.Client{Transport: transport}
	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, 2, resp.ProtoMajor)
	require.Equal(t, int64(1), proxyCalls.Load())
	require.True(t, authSeen.Load(), "proxy did not receive Proxy-Authorization")
}

func TestCPAChromeUnsupportedProxyFailsClosed(t *testing.T) {
	proxyURL, err := url.Parse("ftp://127.0.0.1:1")
	require.NoError(t, err)
	dialer := tlsfingerprint.NewCPAChromeDialer(proxyURL, tlsfingerprint.NewCPAChromeSessionCache(), true)
	_, err = dialer.DialTLSContext(t.Context(), "tcp", "example.com:443", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported CPA proxy scheme")
}

func startTestHTTPConnectProxy(t *testing.T) (string, *atomic.Int64) {
	proxyURL, calls, _ := startTestHTTPConnectProxyWithAuth(t)
	return proxyURL, calls
}

func startTestHTTPConnectProxyWithAuth(t *testing.T) (string, *atomic.Int64, *atomic.Bool) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	calls := &atomic.Int64{}
	authSeen := &atomic.Bool{}
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			calls.Add(1)
			go serveTestHTTPConnectConn(conn, authSeen)
		}
	}()
	return "http://127.0.0.1:" + fmt.Sprintf("%d", listener.Addr().(*net.TCPAddr).Port), calls, authSeen
}

func serveTestHTTPConnectConn(client net.Conn, authSeen *atomic.Bool) {
	defer func() { _ = client.Close() }()
	reader := bufio.NewReader(client)
	req, err := http.ReadRequest(reader)
	if err != nil || req.Method != http.MethodConnect {
		return
	}
	if strings.TrimSpace(req.Header.Get("Proxy-Authorization")) != "" {
		authSeen.Store(true)
	}
	target, err := net.Dial("tcp", req.Host)
	if err != nil {
		_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer func() { _ = target.Close() }()
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	go func() { _, _ = io.Copy(target, reader); _ = target.Close() }()
	_, _ = io.Copy(client, target)
}
