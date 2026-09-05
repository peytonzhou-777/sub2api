package tlsfingerprint

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	utls "github.com/refraction-networking/utls"
)

func TestCPAChromeWSClientHelloExtensions(t *testing.T) {
	conn := utls.UClient(nil, &utls.Config{ServerName: "localhost"}, utls.HelloChrome_Auto)
	if err := configureCPAChromeWSClientHello(conn); err != nil {
		t.Fatal(err)
	}
	// 再次构建模拟 HandshakeContext，防止模板重建覆盖已收敛的扩展。
	if err := conn.BuildHandshakeState(); err != nil {
		t.Fatal(err)
	}
	if got := conn.HandshakeState.Hello.AlpnProtocols; !slices.Equal(got, []string{"http/1.1"}) {
		t.Fatalf("actual ClientHello ALPN = %v", got)
	}
	for _, extension := range conn.Extensions {
		switch extension.(type) {
		case *utls.ApplicationSettingsExtension, *utls.ApplicationSettingsExtensionNew:
			t.Fatal("WS ClientHello must not advertise H2 application settings")
		}
	}
}

func TestCPAChromeWSUpgradeNegotiatesHTTP1(t *testing.T) {
	for _, tc := range []struct {
		name       string
		proxy      bool
		serverALPN []string
		wantALPN   string
	}{
		{"direct", false, []string{"h2", "http/1.1"}, "http/1.1"},
		{"connect_proxy", true, []string{"h2", "http/1.1"}, "http/1.1"},
		{"no_alpn", false, []string{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			offered := make(chan []string, 2)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Proto != "HTTP/1.1" || r.TLS.NegotiatedProtocol != tc.wantALPN {
					t.Errorf("Upgrade protocol = %s, ALPN = %q", r.Proto, r.TLS.NegotiatedProtocol)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				for range 2 {
					kind, payload, err := conn.Read(ctx)
					if err != nil {
						t.Error(err)
						return
					}
					if err := conn.Write(ctx, kind, payload); err != nil {
						t.Error(err)
						return
					}
				}
			}))
			server.EnableHTTP2 = true
			server.TLS = &tls.Config{NextProtos: tc.serverALPN, GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
				offered <- slices.Clone(hello.SupportedProtos)
				return nil, nil
			}}
			server.StartTLS()
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			var proxyURL *url.URL
			if tc.proxy {
				proxyURL = newCPAChromeTestConnectProxy(t, server.Listener.Addr().String())
			}
			dialer := NewCPAChromeWSH1Dialer(proxyURL, NewCPAChromeSessionCache())
			transport := &http.Transport{
				ForceAttemptHTTP2: false,
				DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.DialTLSContext(ctx, network, addr, &tls.Config{RootCAs: roots})
				},
			}
			defer transport.CloseIdleConnections()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, response, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(server.URL, "https"), &websocket.DialOptions{
				HTTPClient: &http.Client{Transport: transport},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if response.StatusCode != http.StatusSwitchingProtocols {
				t.Fatalf("Upgrade status = %d", response.StatusCode)
			}
			if got := <-offered; !slices.Equal(got, []string{"http/1.1"}) {
				t.Fatalf("on-wire ClientHello ALPN = %v", got)
			}
			for _, payload := range []string{"first turn", "second turn"} {
				if err := conn.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
					t.Fatal(err)
				}
				kind, reply, err := conn.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if kind != websocket.MessageText || string(reply) != payload {
					t.Fatalf("unexpected WS reply: %d %q", kind, reply)
				}
			}
		})
	}
}

func TestCPAChromeRESTNegotiatesHTTP2(t *testing.T) {
	offered := make(chan []string, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		offered <- slices.Clone(hello.SupportedProtos)
		return nil, nil
	}}
	server.StartTLS()
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dialer := NewCPAChromeDialer(nil, NewCPAChromeSessionCache(), true)
	conn, err := dialer.DialTLSContext(ctx, "tcp", server.Listener.Addr().String(), &tls.Config{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := <-offered; !slices.Equal(got, []string{"h2", "http/1.1"}) {
		t.Fatalf("REST ClientHello ALPN = %v", got)
	}
	if got := conn.(*utls.UConn).ConnectionState().NegotiatedProtocol; got != "h2" {
		t.Fatalf("REST negotiated protocol = %q", got)
	}
}

// newCPAChromeTestConnectProxy 仅转发到指定本机测试服务，覆盖真实 CONNECT 隧道。
func newCPAChromeTestConnectProxy(t *testing.T, target string) *url.URL {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != target {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		upstream, err := net.DialTimeout("tcp", target, 5*time.Second)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		client, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer client.Close()
		if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			t.Error(err)
			return
		}
		if err := buffered.Flush(); err != nil {
			t.Error(err)
			return
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = io.Copy(upstream, buffered)
			_ = upstream.Close()
		}()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
		<-done
	}))
	t.Cleanup(server.Close)
	proxyURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return proxyURL
}
