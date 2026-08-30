package tlsfingerprint

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/proxy"
)

// CPAChromeProfileVersion is the application-level version of the locked
// Chrome TLS profile used by OpenAI OAuth transports.
const CPAChromeProfileVersion = "openai_chrome_cpa_v1"

// NewCPAChromeSessionCache returns a small per-transport TLS session cache.
// Callers must create one cache for each complete transport scope and must not
// share it across Persona, slot, epoch, credential chain or profile changes.
func NewCPAChromeSessionCache() utls.ClientSessionCache {
	return utls.NewLRUClientSessionCache(64)
}

// CPAChromeDialer establishes a target TLS connection using the CPA-verified
// Chrome ClientHello and an optional CPA-compatible proxy route.
//
// The dialer intentionally owns only transport concerns. Persona headers,
// request bodies, OAuth credentials and Session/Thread identifiers remain in
// the caller's HTTP/WS adapter.
type CPAChromeDialer struct {
	proxyURL     *url.URL
	dialer       *net.Dialer
	sessionCache utls.ClientSessionCache
	requireH2    bool
	nextProtos   []string
}

// NewCPAChromeDialer creates a context-aware Chrome TLS dialer.
// sessionCache must be scoped by the caller (normally one Account × Persona ×
// Slot × Epoch × Credential Chain scope) and is never shared globally.
func NewCPAChromeDialer(proxyURL *url.URL, sessionCache utls.ClientSessionCache, requireH2 bool) *CPAChromeDialer {
	return &CPAChromeDialer{
		proxyURL:     proxyURL,
		dialer:       &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
		sessionCache: sessionCache,
		requireH2:    requireH2,
	}
}

// NewCPAChromeWSH1Dialer returns a CPA Chrome dialer whose ALPN is pinned to
// HTTP/1.1 for the WebSocket Upgrade handshake. The TLS ClientHello remains
// the same Chrome profile; only the negotiated application protocol differs
// from the HTTP/2 REST transport.
func NewCPAChromeWSH1Dialer(proxyURL *url.URL, sessionCache utls.ClientSessionCache) *CPAChromeDialer {
	dialer := NewCPAChromeDialer(proxyURL, sessionCache, false)
	dialer.nextProtos = []string{"http/1.1"}
	return dialer
}

// DialTLSContext implements the signature required by http2.Transport.
func (d *CPAChromeDialer) DialTLSContext(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
	if d == nil {
		return nil, errors.New("CPA Chrome dialer is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	conn, err := d.dialProxyRoute(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	stopWatcher := watchConnContext(ctx, conn)
	defer stopWatcher()

	host := addr
	if parsedHost, _, splitErr := net.SplitHostPort(addr); splitErr == nil {
		host = parsedHost
	}
	utlsConfig := cloneUTLSConfig(cfg, host, d.sessionCache)
	if len(d.nextProtos) > 0 {
		utlsConfig.NextProtos = append([]string(nil), d.nextProtos...)
	}
	utlsConn := utls.UClient(conn, utlsConfig, utls.HelloChrome_Auto)
	if err := utlsConn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("CPA Chrome TLS handshake failed: %w", err)
	}
	state := utlsConn.ConnectionState()
	if d.requireH2 && state.NegotiatedProtocol != "h2" {
		_ = utlsConn.Close()
		return nil, fmt.Errorf("CPA Chrome TLS negotiated %q, want h2", state.NegotiatedProtocol)
	}
	return utlsConn, nil
}

// DialTLSContextHTTP1 adapts the CPA dialer to net/http.Transport's TLS
// callback signature for WebSocket HTTP/1.1 Upgrade handshakes.
func (d *CPAChromeDialer) DialTLSContextHTTP1(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.DialTLSContext(ctx, network, addr, nil)
}

func cloneUTLSConfig(cfg *tls.Config, host string, cache utls.ClientSessionCache) *utls.Config {
	result := &utls.Config{
		ServerName:         host,
		NextProtos:         []string{"h2", "http/1.1"},
		MinVersion:         tls.VersionTLS12,
		MaxVersion:         tls.VersionTLS13,
		ClientSessionCache: cache,
	}
	if cfg == nil {
		return result
	}
	if strings.TrimSpace(cfg.ServerName) != "" {
		result.ServerName = cfg.ServerName
	}
	if len(cfg.NextProtos) > 0 {
		result.NextProtos = append([]string(nil), cfg.NextProtos...)
	}
	if cfg.MinVersion != 0 {
		result.MinVersion = cfg.MinVersion
	}
	if cfg.MaxVersion != 0 {
		result.MaxVersion = cfg.MaxVersion
	}
	result.RootCAs = cfg.RootCAs
	result.InsecureSkipVerify = cfg.InsecureSkipVerify
	result.ClientSessionCache = cache
	return result
}

func (d *CPAChromeDialer) dialProxyRoute(ctx context.Context, network, addr string) (net.Conn, error) {
	if d.proxyURL == nil {
		return d.dialer.DialContext(ctx, network, addr)
	}
	scheme := strings.ToLower(strings.TrimSpace(d.proxyURL.Scheme))
	switch scheme {
	case "http", "https":
		return d.dialHTTPConnect(ctx, network, addr, scheme == "https")
	case "socks5", "socks5h":
		return d.dialSOCKS(ctx, network, addr)
	default:
		return nil, fmt.Errorf("unsupported CPA proxy scheme: %s", scheme)
	}
}

func (d *CPAChromeDialer) dialHTTPConnect(ctx context.Context, network, addr string, secureProxy bool) (net.Conn, error) {
	proxyAddr := d.proxyURL.Host
	if d.proxyURL.Port() == "" {
		if secureProxy {
			proxyAddr = net.JoinHostPort(d.proxyURL.Hostname(), "443")
		} else {
			proxyAddr = net.JoinHostPort(d.proxyURL.Hostname(), "80")
		}
	}
	conn, err := d.dialer.DialContext(ctx, network, proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("connect to CPA proxy: %w", err)
	}
	stopWatcher := watchConnContext(ctx, conn)
	defer stopWatcher()

	if secureProxy {
		proxyTLS := tls.Client(conn, &tls.Config{ServerName: d.proxyURL.Hostname(), MinVersion: tls.VersionTLS12})
		if err := proxyTLS.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("CPA proxy TLS handshake failed: %w", err)
		}
		conn = proxyTLS
	}

	connectReq := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}
	if d.proxyURL.User != nil {
		user := d.proxyURL.User.Username()
		password, _ := d.proxyURL.User.Password()
		connectReq.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+password)))
	}
	if err := connectReq.Write(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write CPA proxy CONNECT: %w", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), connectReq)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read CPA proxy CONNECT response: %w", err)
	}
	// Do not close resp.Body here: for a CONNECT response it is backed by the
	// same tunnel connection that must be returned to the caller.
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("CPA proxy CONNECT failed: %s", resp.Status)
	}
	return conn, nil
}

func (d *CPAChromeDialer) dialSOCKS(ctx context.Context, network, addr string) (net.Conn, error) {
	var auth *proxy.Auth
	if d.proxyURL.User != nil {
		password, _ := d.proxyURL.User.Password()
		auth = &proxy.Auth{User: d.proxyURL.User.Username(), Password: password}
	}
	socksDialer, err := proxy.SOCKS5(network, d.proxyURL.Host, auth, d.dialer)
	if err != nil {
		return nil, fmt.Errorf("create CPA SOCKS5 dialer: %w", err)
	}
	if contextDialer, ok := socksDialer.(proxy.ContextDialer); ok {
		return contextDialer.DialContext(ctx, network, addr)
	}
	return dialWithContext(ctx, socksDialer, network, addr)
}

func dialWithContext(ctx context.Context, dialer proxy.Dialer, network, addr string) (net.Conn, error) {
	result := make(chan struct {
		conn net.Conn
		err  error
	}, 1)
	go func() {
		conn, err := dialer.Dial(network, addr)
		result <- struct {
			conn net.Conn
			err  error
		}{conn: conn, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case outcome := <-result:
		return outcome.conn, outcome.err
	}
}

func watchConnContext(ctx context.Context, conn net.Conn) func() {
	if ctx == nil || conn == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}
