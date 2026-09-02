package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAIWSLeaseLossAfterReadConn struct {
	*openAIWSCaptureConn
	cancel context.CancelCauseFunc
	once   sync.Once
}

func (c *openAIWSLeaseLossAfterReadConn) ReadMessage(ctx context.Context) ([]byte, error) {
	message, err := c.openAIWSCaptureConn.ReadMessage(ctx)
	if err == nil {
		c.once.Do(func() {
			c.cancel(ErrOpenAIWSIngressLeaseLost)
		})
	}
	return message, err
}

type openAIWSSingleConnDialer struct {
	conn openAIWSClientConn
}

func (d *openAIWSSingleConnDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
) (openAIWSClientConn, int, http.Header, error) {
	return d.conn, 0, nil, nil
}

func TestOpenAIWSDownstreamWriteContext_CancellationOwnership(t *testing.T) {
	t.Run("pre-canceled ordinary context is canceled before return", func(t *testing.T) {
		controlCtx, cancelControl := context.WithCancelCause(context.Background())
		cancelControl(context.Canceled)

		writeCtx, cancelWrite := newOpenAIWSDownstreamWriteContext(controlCtx, nil, time.Second)
		defer cancelWrite()
		require.ErrorIs(t, writeCtx.Err(), context.Canceled)
	})

	t.Run("lease loss keeps current write alive", func(t *testing.T) {
		lifecycleCtx, cancelLifecycle := context.WithCancelCause(context.Background())
		controlCtx, cancelControl := context.WithCancelCause(lifecycleCtx)
		hooks := &OpenAIWSIngressHooks{ClientLifecycleContext: lifecycleCtx}
		writeCtx, cancelWrite := newOpenAIWSDownstreamWriteContext(controlCtx, hooks, time.Second)
		defer cancelWrite()

		cancelControl(ErrOpenAIWSIngressLeaseLost)
		select {
		case <-writeCtx.Done():
			t.Fatalf("lease loss unexpectedly canceled downstream write: %v", writeCtx.Err())
		case <-time.After(20 * time.Millisecond):
		}

		clientDisconnected := errors.New("client disconnected")
		cancelLifecycle(clientDisconnected)
		<-writeCtx.Done()
		require.ErrorIs(t, context.Cause(writeCtx), clientDisconnected)
	})

	t.Run("ordinary cancellation is direct and preserves cause", func(t *testing.T) {
		lifecycleCtx, cancelLifecycle := context.WithCancelCause(context.Background())
		controlCtx, cancelControl := context.WithCancelCause(lifecycleCtx)
		defer cancelControl(context.Canceled)
		hooks := &OpenAIWSIngressHooks{ClientLifecycleContext: lifecycleCtx}
		writeCtx, cancelWrite := newOpenAIWSDownstreamWriteContext(controlCtx, hooks, time.Second)
		defer cancelWrite()

		serverShutdown := errors.New("server shutdown")
		cancelLifecycle(serverShutdown)
		require.ErrorIs(t, writeCtx.Err(), context.Canceled)
		require.ErrorIs(t, context.Cause(writeCtx), serverShutdown)
	})
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_KeepLeaseAcrossTurns(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.output_item.done","item":{"id":"ig_ingress_1","type":"image_generation_call","status":"generating","result":"iVBORw0KGgoAAAANSUhEUg/+=="}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_ingress_turn_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_ingress_turn_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          114,
		Name:        "openai-ingress-session-lease",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}
	serverErrCh := make(chan error, 1)
	turnTerminalCh := make(chan string, 2)
	turnAcceptedCh := make(chan int, 2)
	hooks := &OpenAIWSIngressHooks{
		OnTurnAccepted: func(turn int) {
			turnAcceptedCh <- turn
		},
		AfterTurn: func(_ int, result *OpenAIForwardResult, turnErr error) {
			if turnErr == nil && result != nil {
				turnTerminalCh <- result.UpstreamTerminalEvent
			}
		},
	}
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false}`)
	firstTurnImageEvent := readMessage()
	require.Equal(t, "response.output_item.done", gjson.GetBytes(firstTurnImageEvent, "type").String())
	require.Equal(t, "completed", gjson.GetBytes(firstTurnImageEvent, "item.status").String())
	require.Equal(t, "iVBORw0KGgoAAAANSUhEUg/+==", gjson.GetBytes(firstTurnImageEvent, "item.result").String())
	firstTurnEvent := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(firstTurnEvent, "type").String())
	require.Equal(t, "resp_ingress_turn_1", gjson.GetBytes(firstTurnEvent, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_ingress_turn_1"}`)
	secondTurnEvent := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(secondTurnEvent, "type").String())
	require.Equal(t, "resp_ingress_turn_2", gjson.GetBytes(secondTurnEvent, "response.id").String())
	require.Equal(t, 1, <-turnAcceptedCh, "首轮 turn 应触发 accepted")
	require.Equal(t, 2, <-turnAcceptedCh, "第二轮 turn 应触发 accepted")
	require.Equal(t, "response.completed", <-turnTerminalCh, "首轮 turn 应保留成功终态")
	require.Equal(t, "response.completed", <-turnTerminalCh, "第二轮 turn 应保留成功终态")

	_ = clientConn.Close(coderws.StatusNormalClosure, "done")

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	metrics := svc.SnapshotOpenAIWSPoolMetrics()
	require.Equal(t, int64(1), metrics.AcquireTotal, "同一 ingress 会话多 turn 应只获取一次上游 lease")
	require.Equal(t, 1, captureDialer.DialCount(), "同一 ingress 会话应保持同一上游连接")
	require.Len(t, captureConn.writes, 2, "应向同一上游连接发送两轮 response.create")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_LeaseLossSendsRetryClose(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	lifecycleCtx, cancelLifecycle := context.WithCancelCause(context.Background())
	defer cancelLifecycle(context.Canceled)
	controlCtx, cancelControl := context.WithCancelCause(lifecycleCtx)
	upstreamConn := &openAIWSLeaseLossAfterReadConn{
		openAIWSCaptureConn: &openAIWSCaptureConn{events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_lease_loss","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		}},
		cancel: cancelControl,
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSSingleConnDialer{conn: upstreamConn})
	defer pool.Close()

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	account := &Account{
		ID:          118,
		Name:        "openai-ingress-lease-loss",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra:       map[string]any{"responses_websockets_v2_enabled": true},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(controlCtx)
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancelRead()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(
			controlCtx,
			ginCtx,
			conn,
			account,
			"sk-test",
			firstMessage,
			&OpenAIWSIngressHooks{ClientLifecycleContext: lifecycleCtx},
		)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	msgType, event, err := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, coderws.MessageText, msgType)
	require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())

	closeReadCtx, cancelCloseRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = clientConn.Read(closeReadCtx)
	cancelCloseRead()
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.Code)
	require.Equal(t, "websocket ingress capacity lease lost; please reconnect", closeErr.Reason)

	select {
	case serverErr := <-serverErrCh:
		var clientCloseErr *OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &clientCloseErr)
		require.Equal(t, coderws.StatusTryAgainLater, clientCloseErr.StatusCode())
		require.ErrorIs(t, serverErr, ErrOpenAIWSIngressLeaseLost)
	case <-time.After(3 * time.Second):
		t.Fatal("ingress lease-loss reader did not exit")
	}
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_IdleTimeoutReleasesStoreDisabledSession(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.IngressInterTurnIdleTimeoutSeconds = 1
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_idle_timeout","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)
	defer pool.Close()
	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	account := &Account{
		ID:          116,
		Name:        "openai-ingress-idle-timeout",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra:       map[string]any{"responses_websockets_v2_enabled": true},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		_, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			serverErrCh <- err
			return
		}
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = r.Clone(r.Context())
		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, err := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())

	closeReadCtx, cancelCloseRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = clientConn.Read(closeReadCtx)
	cancelCloseRead()
	var clientClose coderws.CloseError
	require.ErrorAs(t, err, &clientClose)
	require.Equal(t, coderws.StatusNormalClosure, clientClose.Code)
	require.Equal(t, "websocket idle timeout", clientClose.Reason)

	select {
	case proxyErr := <-serverErrCh:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, proxyErr, &closeErr)
		require.Equal(t, coderws.StatusNormalClosure, closeErr.StatusCode())
		require.Equal(t, "websocket idle timeout", closeErr.Reason())
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for idle ingress session to close")
	}

	ap, ok := pool.getAccountPool(account.ID)
	require.True(t, ok)
	ap.mu.Lock()
	require.Empty(t, ap.pinnedConns, "idle close must unpin a store=false session")
	for _, conn := range ap.conns {
		require.False(t, conn.isLeased(), "idle close must release the upstream lease")
	}
	ap.mu.Unlock()
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_FollowupCreateCanOmitModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_omit_model_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_omit_model_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)
	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	account := &Account{
		ID:          115,
		Name:        "openai-ingress-omit-model",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
			"model_mapping": map[string]any{
				"client-model": "gpt-5.1",
			},
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"client-model","stream":false}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, firstEvent, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, "resp_omit_model_1", gjson.GetBytes(firstEvent, "response.id").String())

	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","stream":false,"previous_response_id":"resp_omit_model_1"}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead = context.WithTimeout(context.Background(), 3*time.Second)
	_, secondEvent, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, "resp_omit_model_2", gjson.GetBytes(secondEvent, "response.id").String())
	_ = clientConn.Close(coderws.StatusNormalClosure, "done")

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Len(t, captureConn.writes, 2)
	require.Equal(t, "gpt-5.1", gjson.Get(requestToJSONString(captureConn.writes[0]), "model").String())
	require.Equal(t, "gpt-5.1", gjson.Get(requestToJSONString(captureConn.writes[1]), "model").String())
	require.Equal(t, "resp_omit_model_1", gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_CodexImageBridgeRespectsResponsesLite(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_codex_image_bridge","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_codex_image_lite","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_codex_image_function","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	configureOpenAICodexGatewayTest(svc)

	groupID := int64(3)
	apiKey := &APIKey{
		ID:      1,
		UserID:  1,
		GroupID: &groupID,
		Group: &Group{
			ID:                   groupID,
			AllowImageGeneration: true,
		},
	}
	account := &Account{
		ID:          31,
		Name:        "openai-codex-image-ws",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "test-token",
		},
		Extra: map[string]any{
			"openai_oauth_responses_websockets_v2_enabled": true,
			"codex_image_generation_bridge":                true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "codex_cli_rs/0.98.0")
		ginCtx.Request = req
		ginCtx.Set("api_key", apiKey)

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "test-token", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.5","stream":false,"input":"draw a cat","parallel_tool_calls":true,"sequence":900719925474099312345}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	msgType, message, err := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, coderws.MessageText, msgType)
	require.Equal(t, "resp_codex_image_bridge", gjson.GetBytes(message, "response.id").String())

	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{
		"type":"response.create",
		"model":"gpt-5.5",
		"stream":false,
		"previous_response_id":"resp_codex_image_bridge",
		"reasoning":{"effort":"high"},
		"parallel_tool_calls":true,
		"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},
		"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}],
		"input":[
			{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec","description":"Execute code-mode tools, including image_gen.imagegen."}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"draw a cat"}]}
		],
		"tool_choice":{"type":"namespace","name":"collaboration"}
	}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead = context.WithTimeout(context.Background(), 3*time.Second)
	msgType, message, err = clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, coderws.MessageText, msgType)
	require.Equal(t, "resp_codex_image_lite", gjson.GetBytes(message, "response.id").String())

	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{
		"type":"response.create",
		"model":"gpt-5.5",
		"stream":false,
		"previous_response_id":"resp_codex_image_lite",
		"input":"draw a cat",
		"tools":[{"type":"function","name":"image_gen.imagegen","parameters":{"type":"object"}}]
	}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead = context.WithTimeout(context.Background(), 3*time.Second)
	msgType, message, err = clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, coderws.MessageText, msgType)
	require.Equal(t, "resp_codex_image_function", gjson.GetBytes(message, "response.id").String())

	writeCtx, cancelWrite = context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{
		"type":"response.create",
		"model":"gpt-5.5",
		"parallel_tool_calls":"false",
		"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},
		"tools":[{"type":"function","name":"shell"}]
	}`))
	cancelWrite()
	require.NoError(t, err)

	select {
	case serverErr := <-serverErrCh:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
		require.Contains(t, closeErr.Reason(), "parallel_tool_calls to be a boolean")
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Len(t, captureConn.writes, 3)
	nonLitePayload := requestToJSONString(captureConn.writes[0])
	require.True(t, gjson.Get(nonLitePayload, `tools.#(type=="image_generation")`).Exists())
	require.Equal(t, "png", gjson.Get(nonLitePayload, `tools.#(type=="image_generation").output_format`).String())
	require.Equal(t, "auto", gjson.Get(nonLitePayload, "tool_choice").String())
	require.Contains(t, gjson.Get(nonLitePayload, "instructions").String(), "image_generation")
	require.False(t, gjson.Get(nonLitePayload, "reasoning.context").Exists())
	require.True(t, gjson.Get(nonLitePayload, "parallel_tool_calls").Bool())
	require.Equal(t, "900719925474099312345", gjson.Get(nonLitePayload, "sequence").Raw)

	litePayload := requestToJSONString(captureConn.writes[1])
	require.False(t, gjson.Get(litePayload, `tools.#(type=="image_generation")`).Exists())
	require.NotContains(t, gjson.Get(litePayload, "instructions").String(), "image_generation")
	require.Equal(t, "exec", gjson.Get(litePayload, `input.#(type=="additional_tools").tools.0.name`).String())
	require.Contains(t, gjson.Get(litePayload, `input.#(type=="additional_tools").tools.0.description`).String(), "image_gen.imagegen")
	require.False(t, gjson.Get(litePayload, `tools.#(type=="namespace")`).Exists())
	require.Equal(t, "collaboration", gjson.Get(litePayload, `input.#(type=="additional_tools").tools.1.name`).String())
	require.Equal(t, "namespace", gjson.Get(litePayload, "tool_choice.type").String())
	require.Equal(t, "collaboration", gjson.Get(litePayload, "tool_choice.name").String())
	require.Equal(t, "high", gjson.Get(litePayload, "reasoning.effort").String())
	require.Equal(t, "all_turns", gjson.Get(litePayload, "reasoning.context").String())
	require.True(t, gjson.Get(litePayload, "parallel_tool_calls").Exists())
	require.False(t, gjson.Get(litePayload, "parallel_tool_calls").Bool())

	functionPayload := requestToJSONString(captureConn.writes[2])
	require.True(t, gjson.Get(functionPayload, `tools.#(name=="image_gen.imagegen")`).Exists())
	require.False(t, gjson.Get(functionPayload, `tools.#(type=="image_generation")`).Exists())
	require.False(t, gjson.Get(functionPayload, "tool_choice").Exists())
	require.NotContains(t, gjson.Get(functionPayload, "instructions").String(), codexImageGenerationBridgeMarker)
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_DedicatedModeDoesNotReuseConnAcrossSessions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeShared
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	upstreamConn1 := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_dedicated_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	upstreamConn2 := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_dedicated_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{upstreamConn1, upstreamConn2},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          441,
		Name:        "openai-ingress-dedicated",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeDedicated,
		},
	}

	serverErrCh := make(chan error, 2)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	runSingleTurnSession := func(expectedResponseID string) {
		dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
		clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
		cancelDial()
		require.NoError(t, err)
		defer func() {
			_ = clientConn.CloseNow()
		}()

		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		msgType, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		require.Equal(t, expectedResponseID, gjson.GetBytes(event, "response.id").String())

		require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))

		select {
		case serverErr := <-serverErrCh:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			t.Fatal("等待 ingress websocket 结束超时")
		}
	}

	runSingleTurnSession("resp_dedicated_1")
	runSingleTurnSession("resp_dedicated_2")

	require.Equal(t, 2, dialer.DialCount(), "dedicated 模式下跨客户端会话不应复用上游连接")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PassthroughModeRelaysByCaddyAdapter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	upstreamConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_passthrough_turn_1","model":"gpt-5.1","output":[{"id":"ig_passthrough_1","type":"image_generation_call","status":"generating","result":"final-image"}],"usage":{"input_tokens":2,"output_tokens":3}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: upstreamConn}
	svc := &OpenAIGatewayService{
		cfg:                       cfg,
		httpUpstream:              &httpUpstreamRecorder{},
		cache:                     &stubGatewayCache{},
		openaiWSResolver:          NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:             NewCodexToolCorrector(),
		openaiWSPassthroughDialer: captureDialer,
	}

	account := &Account{
		ID:          452,
		Name:        "openai-ingress-passthrough",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
		},
	}

	serverErrCh := make(chan error, 1)
	resultCh := make(chan *OpenAIForwardResult, 1)
	hooks := &OpenAIWSIngressHooks{
		AfterTurn: func(_ int, result *OpenAIForwardResult, turnErr error) {
			if turnErr == nil && result != nil {
				resultCh <- result
			}
		},
	}

	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"service_tier":"fast","reasoning":{"effort":"HIGH"}}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
	require.Equal(t, "resp_passthrough_turn_1", gjson.GetBytes(event, "response.id").String())
	require.Equal(t, "completed", gjson.GetBytes(event, "response.output.0.status").String())
	_ = clientConn.Close(coderws.StatusNormalClosure, "done")

	select {
	case serverErr := <-serverErrCh:
		// After normal client close, the server goroutine may receive the close frame
		// as an error — this is expected behavior, not a test failure.
		if serverErr != nil {
			require.Contains(t, serverErr.Error(), "StatusNormalClosure",
				"server error should only be a normal close frame, got: %v", serverErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待 passthrough websocket 结束超时")
	}

	select {
	case result := <-resultCh:
		require.Equal(t, "resp_passthrough_turn_1", result.RequestID)
		require.True(t, result.OpenAIWSMode)
		require.Equal(t, 2, result.Usage.InputTokens)
		require.Equal(t, 3, result.Usage.OutputTokens)
		require.NotNil(t, result.ServiceTier)
		require.Equal(t, "priority", *result.ServiceTier)
		require.NotNil(t, result.ReasoningEffort)
		require.Equal(t, "high", *result.ReasoningEffort)
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 passthrough turn 结果回调")
	}

	require.Equal(t, 1, captureDialer.DialCount(), "passthrough 模式应直接建立上游 websocket")
	require.Len(t, upstreamConn.writes, 1, "passthrough 模式应透传首条 response.create")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PassthroughBridgeBoundaries(t *testing.T) {
	tests := []struct {
		name            string
		payload         string
		threshold       int64
		wantRelayReject bool
	}{
		{
			name:      "small response create",
			payload:   `{"type":"response.create","model":"gpt-5.1"}`,
			threshold: 1024,
		},
		{
			name:      "continuation response create",
			payload:   `{"type":"response.create","previous_response_id":"resp_previous","model":"gpt-5.1"}`,
			threshold: 1,
		},
		{
			name:      "other event type",
			payload:   `{"type":"session.update","padding":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`,
			threshold: 1,
		},
		{
			name:            "malformed data",
			payload:         `{"type":"response.create","padding":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"`,
			threshold:       1,
			wantRelayReject: true,
		},
		{
			name:      "duplicate type",
			payload:   `{"type":"response.create","type":"response.create","model":"gpt-5.1"}`,
			threshold: 1,
		},
		{
			name:      "duplicate previous response id",
			payload:   `{"type":"response.create","previous_response_id":null,"previous_response_id":null,"model":"gpt-5.1"}`,
			threshold: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			cfg := &config.Config{}
			cfg.Security.URLAllowlist.Enabled = false
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.APIKeyEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
			cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
			cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
			cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = tt.threshold

			upstreamConn := &openAIWSCaptureConn{events: [][]byte{
				[]byte(`{"type":"response.completed","response":{"id":"resp_duplicate_keys","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			}}
			dialer := &openAIWSCaptureDialer{conn: upstreamConn}
			httpUpstream := &httpUpstreamRecorder{}
			svc := &OpenAIGatewayService{
				cfg:                       cfg,
				httpUpstream:              httpUpstream,
				cache:                     &stubGatewayCache{},
				openaiWSResolver:          NewOpenAIWSProtocolResolver(cfg),
				toolCorrector:             NewCodexToolCorrector(),
				openaiWSPassthroughDialer: dialer,
			}
			account := &Account{
				ID:          453,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Credentials: map[string]any{"api_key": "sk-test"},
				Extra: map[string]any{
					"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
				},
			}

			errCh := make(chan error, 1)
			wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					errCh <- err
					return
				}
				defer func() { _ = conn.CloseNow() }()
				_, firstMessage, err := conn.Read(r.Context())
				if err != nil {
					errCh <- err
					return
				}
				rec := httptest.NewRecorder()
				ginCtx, _ := gin.CreateTestContext(rec)
				ginCtx.Request = r
				errCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
			}))
			defer wsServer.Close()

			clientConn, _, err := coderws.Dial(context.Background(), "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
			require.NoError(t, err)
			defer func() { _ = clientConn.CloseNow() }()
			require.NoError(t, clientConn.Write(context.Background(), coderws.MessageText, []byte(tt.payload)))
			_, event, err := clientConn.Read(context.Background())
			if tt.wantRelayReject {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, "resp_duplicate_keys", gjson.GetBytes(event, "response.id").String())
				require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
			}

			select {
			case proxyErr := <-errCh:
				if tt.wantRelayReject {
					require.Error(t, proxyErr)
				} else if proxyErr != nil {
					require.Contains(t, proxyErr.Error(), "StatusNormalClosure")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("等待 boundary passthrough websocket 结束超时")
			}
			require.Empty(t, httpUpstream.requests, "boundary frame must not use the HTTP bridge")
			if tt.wantRelayReject {
				require.Zero(t, dialer.DialCount())
				require.Empty(t, upstreamConn.writes)
			} else {
				require.Equal(t, 1, dialer.DialCount())
				require.Len(t, upstreamConn.writes, 1)
			}
		})
	}
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PassthroughHeadersUsePromptCacheAndTurnState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(73)
	apiKeyID := int64(7001)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	upstreamConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_passthrough_headers","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: upstreamConn}
	svc := &OpenAIGatewayService{
		cfg:                       cfg,
		httpUpstream:              &httpUpstreamRecorder{},
		cache:                     &stubGatewayCache{},
		openaiWSResolver:          NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:             NewCodexToolCorrector(),
		openaiWSPassthroughDialer: captureDialer,
	}
	configureOpenAICodexGatewayTest(svc)
	account := &Account{
		ID:          453,
		Name:        "openai-ingress-passthrough-headers",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "oauth-token",
		},
		Extra: map[string]any{
			"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "codex_cli_rs/0.98.0")
		req.Header.Set(openAIWSTurnStateHeader, "turn-state-1")
		req.Header.Set(openAIWSTurnMetadataHeader, "turn-meta-1")
		ginCtx.Request = req
		ginCtx.Set("api_key", &APIKey{ID: apiKeyID, GroupID: &groupID})

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}
		sessionHash := svc.GenerateSessionHash(ginCtx, firstMessage)
		if prepareErr := svc.PrepareCodexFingerprintForAdmission(r.Context(), ginCtx, account, firstMessage, false); prepareErr != nil {
			serverErrCh <- prepareErr
			return
		}
		turnScope, scopeOK := openAITurnStateScopeForAttempt(r.Context(), ginCtx, account)
		if !scopeOK {
			serverErrCh <- errors.New("turn-state scope was not prepared")
			return
		}
		if bindErr := svc.getOpenAIWSStateStore().BindTurnStateScope(
			r.Context(), groupID, apiKeyID, sessionHash, "turn-state-1", turnScope, time.Minute,
		); bindErr != nil {
			serverErrCh <- bindErr
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "oauth-token", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"stream":false,
		"prompt_cache_key":"pcache_passthrough",
		"reasoning":{"effort":"medium","context":"current_turn"},
		"parallel_tool_calls":true,
		"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},
		"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}],
		"input":[{"type":"message","role":"user","content":"hello"}],
		"tool_choice":{"type":"namespace","name":"collaboration"}
	}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, "resp_passthrough_headers", gjson.GetBytes(event, "response.id").String())
	_ = clientConn.Close(coderws.StatusNormalClosure, "done")

	select {
	case serverErr := <-serverErrCh:
		if serverErr != nil {
			require.Contains(t, serverErr.Error(), "StatusNormalClosure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待 passthrough websocket 结束超时")
	}

	forwarded := requestToJSONString(upstreamConn.writes[0])
	sessionID := captureDialer.lastHeaders.Get("session_id")
	requireCodexFingerprintV3UUID(t, sessionID)
	require.Equal(t, sessionID, gjson.Get(forwarded, "prompt_cache_key").String())
	require.Equal(t, sessionID, gjson.Get(forwarded, "client_metadata.session_id").String())
	require.Equal(t, "turn-state-1", captureDialer.lastHeaders.Get(openAIWSTurnStateHeader))
	turnMetadata := captureDialer.lastHeaders.Get(openAIWSTurnMetadataHeader)
	require.NotEqual(t, "turn-meta-1", turnMetadata)
	require.Equal(t, sessionID, gjson.Get(turnMetadata, "session_id").String())
	require.NotEmpty(t, gjson.Get(turnMetadata, "installation_id").String())
	requireCodexFingerprintV3UUID(t, gjson.Get(turnMetadata, "thread_id").String())
	requireCodexFingerprintV3UUID(t, gjson.Get(turnMetadata, "turn_id").String())
	require.Equal(t, gjson.Get(turnMetadata, "installation_id").String(), gjson.Get(forwarded, "client_metadata.x-codex-installation-id").String())
	require.Equal(t, gjson.Get(turnMetadata, "thread_id").String(), gjson.Get(forwarded, "client_metadata.thread_id").String())
	require.Len(t, upstreamConn.writes, 1)
	require.False(t, gjson.Get(forwarded, `tools.#(type=="namespace")`).Exists())
	require.Equal(t, "collaboration", gjson.Get(forwarded, `input.#(type=="additional_tools").tools.0.name`).String())
	require.Equal(t, "namespace", gjson.Get(forwarded, "tool_choice.type").String())
	require.Equal(t, "collaboration", gjson.Get(forwarded, "tool_choice.name").String())
	require.Equal(t, "medium", gjson.Get(forwarded, "reasoning.effort").String())
	require.Equal(t, "all_turns", gjson.Get(forwarded, "reasoning.context").String())
	require.True(t, gjson.Get(forwarded, "parallel_tool_calls").Exists())
	require.False(t, gjson.Get(forwarded, "parallel_tool_calls").Bool())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_HTTPBridgeModeRelaysHTTPStream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_bridge_1"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
					"data: {\"type\":\"response.done\",\"response\":{\"id\":\"resp_http_bridge_1\",\"output\":[{\"id\":\"ig_bridge_1\",\"type\":\"image_generation_call\",\"status\":\"in_progress\",\"result\":\"final-image\"}],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"input_tokens_details\":{\"cached_tokens\":1}}}}\n\n" +
					"data: [DONE]\n\n",
			)),
		},
	}
	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     upstream,
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
	}

	account := &Account{
		ID:          552,
		Name:        "openai-ingress-http-bridge",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeHTTPBridge,
		},
	}

	serverErrCh := make(chan error, 1)
	resultCh := make(chan *OpenAIForwardResult, 1)
	hooks := &OpenAIWSIngressHooks{
		AfterTurn: func(_ int, result *OpenAIForwardResult, turnErr error) {
			if turnErr == nil && result != nil {
				resultCh <- result
			}
		},
	}

	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{
		"type":"response.create","model":"gpt-5.1","stream":false,
		"parallel_tool_calls":true,
		"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}
	}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event1, readErr1 := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr1)
	require.Equal(t, "response.output_text.delta", gjson.GetBytes(event1, "type").String())
	require.Equal(t, "hello", gjson.GetBytes(event1, "delta").String())

	readCtx2, cancelRead2 := context.WithTimeout(context.Background(), 3*time.Second)
	_, event2, readErr2 := clientConn.Read(readCtx2)
	cancelRead2()
	require.NoError(t, readErr2)
	require.Equal(t, "response.done", gjson.GetBytes(event2, "type").String())
	require.Equal(t, "resp_http_bridge_1", gjson.GetBytes(event2, "response.id").String())
	require.Equal(t, "completed", gjson.GetBytes(event2, "response.output.0.status").String())

	_ = clientConn.Close(coderws.StatusNormalClosure, "done")

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 http_bridge websocket 结束超时")
	}

	select {
	case result := <-resultCh:
		require.Equal(t, "resp_http_bridge_1", result.RequestID)
		require.True(t, result.OpenAIWSMode)
		require.Equal(t, 2, result.Usage.InputTokens)
		require.Equal(t, 1, result.Usage.OutputTokens)
		require.Equal(t, 1, result.Usage.CacheReadInputTokens)
		require.NotNil(t, result.FirstTokenMs)
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 http_bridge turn 结果回调")
	}

	require.NotNil(t, upstream.lastReq, "http_bridge 模式应调用 HTTP 上游")
	require.Equal(t, "true", upstream.lastReq.Header.Get(responsesLiteHeader))
	require.True(t, gjson.GetBytes(upstream.lastBody, "parallel_tool_calls").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "parallel_tool_calls").Bool())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ModeOffReturnsPolicyViolation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeShared

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     newOpenAIWSConnPool(cfg),
	}

	account := &Account{
		ID:          442,
		Name:        "openai-ingress-off",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeOff,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
	cancelWrite()
	require.NoError(t, err)

	select {
	case serverErr := <-serverErrCh:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
		require.Equal(t, "websocket mode is disabled for this account", closeErr.Reason())
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledPrevResponseStrictDropToFullCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_preflight_rewrite_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_preflight_rewrite_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          140,
		Name:        "openai-ingress-prev-preflight-rewrite",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_preflight_rewrite_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_stale_external","input":[{"type":"input_text","text":"world"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_preflight_rewrite_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount(), "严格增量不成立时应在同一连接内降级为 full create")
	require.Len(t, captureConn.writes, 2)
	secondWrite := requestToJSONString(captureConn.writes[1])
	require.False(t, gjson.Get(secondWrite, "previous_response_id").Exists(), "严格增量不成立时应移除 previous_response_id，改为 full create")
	require.Equal(t, 2, len(gjson.Get(secondWrite, "input").Array()), "严格降级为 full create 时应重放完整 input 上下文")
	require.Equal(t, "hello", gjson.Get(secondWrite, "input.0.text").String())
	require.Equal(t, "world", gjson.Get(secondWrite, "input.1.text").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledStrictDropReadFailureReplaysExactPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSPreflightFailConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_drop_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_drop_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          142,
		Name:        "openai-ingress-prev-strict-drop-before-ping",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_ping_drop_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_stale_external","input":[{"type":"input_text","text":"world"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_ping_drop_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 严格降级后预检换连超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "首事件前读失败应按需新建一次连接")
	require.Equal(t, 2, firstConn.WriteCount(), "健康路径不做预检，应先乐观写入旧连接")
	require.Zero(t, firstConn.PingCount(), "正常请求热路径不得执行 preflight ping")
	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondRawWrites := append([][]byte(nil), secondConn.rawWrites...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1)
	require.Len(t, secondRawWrites, 1)
	require.Equal(t, firstConn.RawWrite(1), secondRawWrites[0], "恢复必须原字节重发首次实际出站帧")
	secondWrite := requestToJSONString(secondWrites[0])
	require.False(t, gjson.Get(secondWrite, "previous_response_id").Exists(), "严格降级后重试应移除 previous_response_id")
	require.Equal(t, 2, len(gjson.Get(secondWrite, "input").Array()))
	require.Equal(t, "hello", gjson.Get(secondWrite, "input.0.text").String())
	require.Equal(t, "world", gjson.Get(secondWrite, "input.1.text").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreEnabledSkipsStrictPrevResponseEval(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_store_enabled_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_store_enabled_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          143,
		Name:        "openai-ingress-store-enabled-skip-strict",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":true}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_store_enabled_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":true,"previous_response_id":"resp_stale_external"}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_store_enabled_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 store=true 场景 websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.Equal(t, "resp_stale_external", gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").String(), "store=true 场景不应触发 store-disabled strict 规则")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledPrevResponsePreflightSkipForFunctionCallOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_preflight_skip_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_preflight_skip_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          141,
		Name:        "openai-ingress-prev-preflight-skip-fco",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_preflight_skip_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_stale_external","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_preflight_skip_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.Equal(t, "resp_stale_external", gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").String(), "function_call_output 场景不应预改写 previous_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledFunctionCallOutputAutoAttachPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          143,
		Name:        "openai-ingress-fco-auto-prev",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_auto_prev_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"function_call_output","call_id":"call_auto_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_auto_prev_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.Equal(t, "resp_auto_prev_1", gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").String(), "function_call_output 缺失 previous_response_id 时应回填上一轮响应 ID")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledToolSearchOutputAutoAttachesPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_tool_search_prev_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_tool_search_prev_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          145,
		Name:        "openai-ingress-tool-search-output-auto-prev",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_tool_search_prev_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"tool_search_output","call_id":"call_search_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_tool_search_prev_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	secondWrite := requestToJSONString(captureConn.writes[1])
	require.Equal(t, "resp_tool_search_prev_1", gjson.Get(secondWrite, "previous_response_id").String(), "tool_search_output 缺失 previous_response_id 时应回填上一轮响应 ID")
	require.Equal(t, "tool_search_output", gjson.Get(secondWrite, "input.0.type").String())
	require.Equal(t, "call_search_1", gjson.Get(secondWrite, "input.0.call_id").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledFunctionCallOutputSkipsAutoAttachWhenLastResponseIDMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_skip_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          144,
		Name:        "openai-ingress-fco-auto-prev-skip",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(firstTurn, "type").String())
	require.Empty(t, gjson.GetBytes(firstTurn, "response.id").String(), "首轮响应不返回 response.id，模拟无法推导续链锚点")

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"function_call_output","call_id":"call_auto_skip_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_auto_prev_skip_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.False(t, gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").Exists(), "上一轮缺失 response.id 时不应自动补齐 previous_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledFunctionCallOutputSkipsAutoAttachWhenToolCallContextPresent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_ctx_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_ctx_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{captureConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          114,
		Name:        "openai-ingress-tool-context",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_auto_prev_ctx_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"function_call","call_id":"call_ctx_1","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"call_ctx_1","output":"ok"},{"type":"message","role":"user","content":[{"type":"input_text","text":"retry"}]}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_auto_prev_ctx_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.False(t, gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").Exists(), "请求已包含 function_call 上下文时不应自动补齐 previous_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledFunctionCallOutputAutoAttachWhenOnlyItemReferencesPresent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_ref_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_ref_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{captureConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          115,
		Name:        "openai-ingress-item-reference",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_auto_prev_ref_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"item_reference","id":"call_ref_1"},{"type":"function_call_output","call_id":"call_ref_1","output":"ok"},{"type":"message","role":"user","content":[{"type":"input_text","text":"retry"}]}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_auto_prev_ref_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.Equal(t, "resp_auto_prev_ref_1", gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").String(), "仅有 item_reference 不足以自包含 function_call_output，应回填上一轮响应 ID")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ReadFailureBeforeAnyEventReconnectsOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSPreflightFailConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          116,
		Name:        "openai-ingress-preflight-ping",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_ping_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_turn_ping_1"}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_ping_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
	require.Equal(t, 2, dialer.DialCount(), "首事件前读失败应只触发一次换连")
	require.Equal(t, 2, firstConn.WriteCount(), "请求应先直接写入被动复用的旧连接")
	require.Zero(t, firstConn.PingCount(), "第二轮前不得执行 preflight ping")
	secondConn.mu.Lock()
	secondRawWrites := append([][]byte(nil), secondConn.rawWrites...)
	secondConn.mu.Unlock()
	require.Len(t, secondRawWrites, 1)
	require.NotEqual(t, firstConn.RawWrite(1), secondRawWrites[0], "换连恢复必须移除旧连接的 previous_response_id")
	require.False(t, gjson.GetBytes(secondRawWrites[0], "previous_response_id").Exists())
	require.Equal(t, "response.create", gjson.GetBytes(secondRawWrites[0], "type").String())
	metrics := svc.SnapshotOpenAIWSRetryMetrics()
	require.Equal(t, int64(1), metrics.IngressCrossConnDetectedTotal)
	require.Equal(t, int64(1), metrics.IngressCrossConnRebuildTotal)
	require.Equal(t, int64(0), metrics.IngressCrossConnBlockedTotal)
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledStrictAffinityReadFailureRebuildsFullInput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSPreflightFailConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_strict_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_strict_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          121,
		Name:        "openai-ingress-preflight-ping-strict-affinity",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_ping_strict_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_turn_ping_strict_1","input":[{"type":"input_text","text":"world"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_ping_strict_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 严格亲和自动恢复后结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "严格亲和请求首读失败后应自动换连一次")
	require.Equal(t, 2, firstConn.WriteCount(), "严格亲和请求也应直接乐观写入，不做预检")
	require.Zero(t, firstConn.PingCount(), "严格亲和请求不得增加 preflight ping")
	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondRawWrites := append([][]byte(nil), secondConn.rawWrites...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1)
	require.Len(t, secondRawWrites, 1)
	require.NotEqual(t, firstConn.RawWrite(1), secondRawWrites[0], "换连后应将连接局部续链改写为自包含输入")
	secondWrite := requestToJSONString(secondWrites[0])
	require.False(t, gjson.Get(secondWrite, "previous_response_id").Exists(), "新连接不得携带旧连接的 previous_response_id")
	require.Equal(t, 2, len(gjson.Get(secondWrite, "input").Array()))
	require.Equal(t, "hello", gjson.Get(secondWrite, "input.0.text").String())
	require.Equal(t, "world", gjson.Get(secondWrite, "input.1.text").String())
	metrics := svc.SnapshotOpenAIWSRetryMetrics()
	require.Equal(t, int64(1), metrics.IngressCrossConnDetectedTotal)
	require.Equal(t, int64(1), metrics.IngressCrossConnRebuildTotal)
	require.Equal(t, int64(0), metrics.IngressCrossConnBlockedTotal)
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledReadFailureRebuildsFullToolContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSPreflightFailConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_replay_ctx_1","model":"gpt-5.1","output":[{"type":"function_call","id":"fc_replay_1","call_id":"call_replay_1","name":"shell","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_replay_ctx_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          128,
		Name:        "openai-ingress-preflight-replay-function-output-with-context",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"call tool"}]}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_ping_replay_ctx_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_turn_ping_replay_ctx_1","input":[{"type":"function_call_output","call_id":"call_replay_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_ping_replay_ctx_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket function_call_output 自包含重放后结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "function_call_output 首读失败后应换新连接一次")
	require.Equal(t, 2, firstConn.WriteCount())
	require.Zero(t, firstConn.PingCount())
	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondRawWrites := append([][]byte(nil), secondConn.rawWrites...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1)
	require.Len(t, secondRawWrites, 1)
	require.NotEqual(t, firstConn.RawWrite(1), secondRawWrites[0], "工具输出换连后应补齐匹配的工具调用上下文")
	secondWrite := requestToJSONString(secondWrites[0])
	require.False(t, gjson.Get(secondWrite, "previous_response_id").Exists(), "新连接不得携带旧连接的 previous_response_id")
	require.Equal(t, 3, len(gjson.Get(secondWrite, "input").Array()))
	require.Equal(t, "message", gjson.Get(secondWrite, "input.0.type").String())
	require.Equal(t, "user", gjson.Get(secondWrite, "input.0.role").String())
	require.Equal(t, "function_call", gjson.Get(secondWrite, "input.1.type").String())
	require.Equal(t, "call_replay_1", gjson.Get(secondWrite, "input.1.call_id").String())
	require.Equal(t, "function_call_output", gjson.Get(secondWrite, "input.2.type").String())
	require.Equal(t, "call_replay_1", gjson.Get(secondWrite, "input.2.call_id").String())
	metrics := svc.SnapshotOpenAIWSRetryMetrics()
	require.Equal(t, int64(1), metrics.IngressCrossConnDetectedTotal)
	require.Equal(t, int64(1), metrics.IngressCrossConnRebuildTotal)
	require.Equal(t, int64(0), metrics.IngressCrossConnBlockedTotal)
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledRecoveryRebuildsPreviousResponseContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSPreflightFailConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_replay_fc_1","model":"gpt-5.1","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_replay_fc_2","model":"gpt-5.1","usage":{"input_tokens":3,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          129,
		Name:        "openai-ingress-preflight-replay-function-output",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"message","role":"user","content":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_ping_replay_fc_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_turn_ping_replay_fc_1","input":[{"type":"message","role":"user","content":"continue"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_ping_replay_fc_2", gjson.GetBytes(secondTurn, "response.id").String())
	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "首事件前传输故障允许同身份恢复一次")
	require.Equal(t, 2, firstConn.WriteCount())
	require.Zero(t, firstConn.PingCount())
	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondRawWrites := append([][]byte(nil), secondConn.rawWrites...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1)
	require.Len(t, secondRawWrites, 1)
	require.False(t, gjson.Get(requestToJSONString(secondWrites[0]), "previous_response_id").Exists(), "新连接不得收到旧连接的 response_id")
	require.Len(t, gjson.Get(requestToJSONString(secondWrites[0]), "input").Array(), 3)
	require.Equal(t, "assistant", gjson.Get(requestToJSONString(secondWrites[0]), "input.1.role").String())
	metrics := svc.SnapshotOpenAIWSRetryMetrics()
	require.Equal(t, int64(1), metrics.IngressTransportRecoveryAttemptsTotal)
	require.Equal(t, int64(1), metrics.IngressTransportRecoverySuccessTotal)
	require.Equal(t, int64(1), metrics.IngressCrossConnDetectedTotal)
	require.Equal(t, int64(1), metrics.IngressCrossConnRebuildTotal)
	require.Equal(t, int64(0), metrics.IngressCrossConnBlockedTotal)
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledRecoveryBlocksMissingToolContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSPreflightFailConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_replay_only_fc_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"No tool call found for function call output with call_id call_replay_1.","param":"input"}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          130,
		Name:        "openai-ingress-preflight-replay-only-function-output",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"function_call","call_id":"call_other","name":"shell","arguments":"{}"},{"type":"function_call_output","call_id":"call_replay_1","output":"ok"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_ping_replay_only_fc_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_turn_ping_replay_only_fc_1","input":[{"type":"input_text","text":"after tool output"}]}`)
	select {
	case serverErr := <-serverErrCh:
		require.Error(t, serverErr)
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &closeErr)
		require.ErrorIs(t, serverErr, errOpenAIWSCrossConnReplayUnavailable)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "首事件前传输故障允许同身份恢复一次")
	require.Equal(t, 2, firstConn.WriteCount())
	require.Zero(t, firstConn.PingCount())
	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondRawWrites := append([][]byte(nil), secondConn.rawWrites...)
	secondConn.mu.Unlock()
	require.Empty(t, secondWrites, "工具调用上下文不完整时不得向新连接发送请求")
	require.Empty(t, secondRawWrites)
	metrics := svc.SnapshotOpenAIWSRetryMetrics()
	require.Equal(t, int64(1), metrics.IngressTransportRecoveryAttemptsTotal)
	require.Equal(t, int64(0), metrics.IngressTransportRecoverySuccessTotal)
	require.Equal(t, int64(1), metrics.IngressTransportRecoveryFailureTotal)
	require.Equal(t, int64(1), metrics.IngressCrossConnDetectedTotal)
	require.Equal(t, int64(0), metrics.IngressCrossConnRebuildTotal)
	require.Equal(t, int64(1), metrics.IngressCrossConnBlockedTotal)
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_WriteFailBeforeDownstreamRetriesOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSWriteFailAfterFirstTurnConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_write_retry_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_write_retry_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          117,
		Name:        "openai-ingress-write-retry",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}
	var hooksMu sync.Mutex
	beforeTurnCalls := make(map[int]int)
	afterTurnCalls := make(map[int]int)
	hooks := &OpenAIWSIngressHooks{
		BeforeTurn: func(turn int) error {
			hooksMu.Lock()
			beforeTurnCalls[turn]++
			hooksMu.Unlock()
			return nil
		},
		AfterTurn: func(turn int, _ *OpenAIForwardResult, _ error) {
			hooksMu.Lock()
			afterTurnCalls[turn]++
			hooksMu.Unlock()
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"message","role":"user","content":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_write_retry_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_turn_write_retry_1","input":[{"type":"message","role":"user","content":"continue"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_write_retry_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
	require.Equal(t, 2, dialer.DialCount(), "第二轮 turn 上游写失败且未写下游时应自动重试并换连")
	hooksMu.Lock()
	beforeTurn1 := beforeTurnCalls[1]
	beforeTurn2 := beforeTurnCalls[2]
	afterTurn1 := afterTurnCalls[1]
	afterTurn2 := afterTurnCalls[2]
	hooksMu.Unlock()
	require.Equal(t, 1, beforeTurn1, "首轮 turn BeforeTurn 应执行一次")
	require.Equal(t, 1, beforeTurn2, "同一 turn 重试不应重复触发 BeforeTurn")
	require.Equal(t, 1, afterTurn1, "首轮 turn AfterTurn 应执行一次")
	require.Equal(t, 1, afterTurn2, "第二轮 turn AfterTurn 应执行一次")
	metrics := svc.SnapshotOpenAIWSRetryMetrics()
	require.Equal(t, int64(1), metrics.IngressTransportRecoveryAttemptsTotal)
	require.Equal(t, int64(1), metrics.IngressTransportRecoverySuccessTotal)
	require.Equal(t, int64(0), metrics.IngressTransportRecoveryFailureTotal)
	secondConn.mu.Lock()
	secondRawWrites := append([][]byte(nil), secondConn.rawWrites...)
	secondConn.mu.Unlock()
	require.Len(t, secondRawWrites, 1)
	require.True(t, gjson.GetBytes(firstConn.RawWrite(1), "previous_response_id").Exists(), "旧连接首次尝试仍应使用连接内续链")
	require.False(t, gjson.GetBytes(secondRawWrites[0], "previous_response_id").Exists(), "换连恢复不得原样发送旧 response_id")
	require.Len(t, gjson.GetBytes(secondRawWrites[0], "input").Array(), 2)
	dialSnapshots := dialer.DialSnapshots()
	require.Len(t, dialSnapshots, 2)
	require.Equal(t, dialSnapshots[0], dialSnapshots[1], "内部恢复前后的上游 URL、代理和握手身份必须一致")
	_, oldBindingExists := svc.getOpenAIWSStateStore().GetResponseConn("resp_turn_write_retry_1")
	require.False(t, oldBindingExists, "恢复时应删除指向失效连接的旧 response 绑定")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_SecondTransportFailureStopsAfterSingleRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := newOpenAIWSIngressRecoveryTestConfig()
	firstConn := &openAIWSWriteFailAfterFirstTurnConn{failOnWrite: true}
	secondConn := &openAIWSWriteFailAfterFirstTurnConn{failOnWrite: true}
	dialer := &openAIWSQueueDialer{conns: []openAIWSClientConn{firstConn, secondConn}}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)
	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	account := newOpenAIWSIngressRecoveryTestAccount(131)
	payload := []byte(`{"type":"response.create", "model":"gpt-5.1", "stream":false, "input":"hello"}`)

	_, serverErr := runOpenAIWSIngressRecoveryTest(t, svc, account, payload, false)
	require.Error(t, serverErr)
	require.Contains(t, serverErr.Error(), "write upstream websocket request")
	require.Equal(t, 2, dialer.DialCount(), "同一 turn 最多执行一次内部恢复")
	require.Equal(t, firstConn.RawWrite(0), secondConn.RawWrite(0), "第二次尝试必须原字节重发")
	metrics := svc.SnapshotOpenAIWSRetryMetrics()
	require.Equal(t, int64(1), metrics.IngressTransportRecoveryAttemptsTotal)
	require.Equal(t, int64(0), metrics.IngressTransportRecoverySuccessTotal)
	require.Equal(t, int64(1), metrics.IngressTransportRecoveryFailureTotal)
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_UpstreamEventThenDisconnectSuppressesRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := newOpenAIWSIngressRecoveryTestConfig()
	firstConn := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.created","response":{"id":"resp_event_seen","model":"gpt-5.1"}}`),
	}}
	secondConn := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_must_not_run","model":"gpt-5.1"}}`),
	}}
	dialer := &openAIWSQueueDialer{conns: []openAIWSClientConn{firstConn, secondConn}}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)
	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	account := newOpenAIWSIngressRecoveryTestAccount(132)
	payload := []byte(`{"type":"response.create","model":"gpt-5.1","stream":true}`)

	event, serverErr := runOpenAIWSIngressRecoveryTest(t, svc, account, payload, true)
	require.Equal(t, "response.created", gjson.GetBytes(event, "type").String())
	require.Error(t, serverErr)
	require.Contains(t, serverErr.Error(), "read upstream websocket event")
	require.Equal(t, 1, dialer.DialCount(), "观察到任意上游事件后不得重放业务请求")
	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Empty(t, secondWrites)
	metrics := svc.SnapshotOpenAIWSRetryMetrics()
	require.Equal(t, int64(0), metrics.IngressTransportRecoveryAttemptsTotal)
	require.Equal(t, int64(1), metrics.IngressTransportRecoverySuppressedTotal)
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PreviousResponseNotFoundIsForwardedWithoutRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.IngressPreviousResponseRecoveryEnabled = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_recover_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":""}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_recover_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          118,
		Name:        "openai-ingress-prev-recovery",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_seed_anchor"}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_prev_recover_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_turn_prev_recover_1"}`)
	secondTurn := readMessage()
	require.Equal(t, "error", gjson.GetBytes(secondTurn, "type").String())
	require.Equal(t, "previous_response_not_found", gjson.GetBytes(secondTurn, "error.code").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, dialer.DialCount(), "合法业务错误事件不得触发传输恢复")

	firstConn.mu.Lock()
	firstWrites := append([]map[string]any(nil), firstConn.writes...)
	firstConn.mu.Unlock()
	require.Len(t, firstWrites, 2, "首个连接应处理首轮与失败的第二轮请求")
	require.True(t, gjson.Get(requestToJSONString(firstWrites[1]), "previous_response_id").Exists(), "失败轮次首发请求应包含 previous_response_id")

	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Empty(t, secondWrites, "业务错误后不得改写负载并换连重放")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledStrictAffinityPreviousResponseNotFoundIsForwarded(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.IngressPreviousResponseRecoveryEnabled = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_strict_recover_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"missing strict anchor"}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_strict_recover_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          122,
		Name:        "openai-ingress-prev-strict-layer2",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"prompt_cache_key":"pk_strict_layer2","input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_prev_strict_recover_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"prompt_cache_key":"pk_strict_layer2","previous_response_id":"resp_turn_prev_strict_recover_1","input":[{"type":"input_text","text":"world"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "error", gjson.GetBytes(secondTurn, "type").String())
	require.Equal(t, "previous_response_not_found", gjson.GetBytes(secondTurn, "error.code").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 严格亲和 Layer2 恢复结束超时")
	}

	require.Equal(t, 1, dialer.DialCount(), "严格亲和链路的业务错误不得触发 Layer2 重放")

	firstConn.mu.Lock()
	firstWrites := append([]map[string]any(nil), firstConn.writes...)
	firstConn.mu.Unlock()
	require.Len(t, firstWrites, 2, "首连接应收到首轮请求和失败的续链请求")
	require.True(t, gjson.Get(requestToJSONString(firstWrites[1]), "previous_response_id").Exists())

	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Empty(t, secondWrites, "不得删除 previous_response_id 后合成完整历史重放")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PreviousResponseNotFoundDoesNotRewriteDuplicatePrevID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.IngressPreviousResponseRecoveryEnabled = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_once_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"first missing"}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_once_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          120,
		Name:        "openai-ingress-prev-recovery-once",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_prev_once_1", gjson.GetBytes(firstTurn, "response.id").String())

	// duplicate previous_response_id 由上游按业务错误处理，sub2api 不改写后重放。
	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_turn_prev_once_1","input":[],"previous_response_id":"resp_turn_prev_duplicate"}`)
	secondTurn := readMessage()
	require.Equal(t, "error", gjson.GetBytes(secondTurn, "type").String())
	require.Equal(t, "previous_response_not_found", gjson.GetBytes(secondTurn, "error.code").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, dialer.DialCount(), "previous_response_not_found 不属于传输恢复范围")

	firstConn.mu.Lock()
	firstWrites := append([]map[string]any(nil), firstConn.writes...)
	firstConn.mu.Unlock()
	require.Len(t, firstWrites, 2)
	require.True(t, gjson.Get(requestToJSONString(firstWrites[1]), "previous_response_id").Exists())

	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Empty(t, secondWrites, "重复键场景也不得合成或裁剪业务帧后重试")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ReconnectUsesReplayCheckpointOnNewUpstreamConn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := newOpenAIWSIngressRecoveryTestConfig()
	newConn := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_reconnected_2","model":"gpt-5.1","usage":{"input_tokens":3,"output_tokens":1}}}`),
	}}
	dialer := &openAIWSQueueDialer{conns: []openAIWSClientConn{newConn}}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)
	stateStore := NewOpenAIWSStateStore(nil)
	svc := &OpenAIGatewayService{
		cfg:                cfg,
		httpUpstream:       &httpUpstreamRecorder{},
		cache:              &stubGatewayCache{},
		openaiWSResolver:   NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:      NewCodexToolCorrector(),
		openaiWSPool:       pool,
		openaiWSStateStore: stateStore,
	}
	account := newOpenAIWSIngressRecoveryTestAccount(133)
	groupID := int64(7001)

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		request := r.Clone(r.Context())
		request.Header = request.Header.Clone()
		request.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = request
		ginCtx.Set("api_key", &APIKey{GroupID: &groupID})

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancelRead()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		decision := svc.openaiWSResolver.Resolve(account)
		wsURL, buildURLErr := svc.buildOpenAIResponsesWSURL(account)
		if buildURLErr != nil {
			serverErrCh <- buildURLErr
			return
		}
		headers, _, buildHeaderErr := svc.buildOpenAIWSHeaders(
			r.Context(), ginCtx, account, "sk-test", decision, false, "", "", "", "gpt-5.1", "",
		)
		if buildHeaderErr != nil {
			serverErrCh <- buildHeaderErr
			return
		}
		target := newOpenAIWSConnectionTarget(account, decision.Transport, wsURL, headers)
		bindOpenAIWSResponseConn(stateStore, "resp_reconnected_1", target, "conn-expired", time.Minute)
		bindOpenAIWSReplayCheckpoint(stateStore, groupID, "resp_reconnected_1", openAIWSReplayCheckpoint{
			SourceConnID:     "conn-expired",
			Target:           target,
			RequestInput:     []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello"}`)},
			RequestInputSeen: true,
			ResponseOutput:   []json.RawMessage{json.RawMessage(`{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hi"}]}`)},
			Replayable:       true,
		}, time.Minute)

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_reconnected_1","input":[{"type":"message","role":"user","content":"continue"}]}`)))
	cancelWrite()
	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, "resp_reconnected_2", gjson.GetBytes(event, "response.id").String())
	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待重连 checkpoint 测试结束超时")
	}
	require.Equal(t, 1, dialer.DialCount())
	newConn.mu.Lock()
	writes := append([]map[string]any(nil), newConn.writes...)
	newConn.mu.Unlock()
	require.Len(t, writes, 1)
	written := requestToJSONString(writes[0])
	require.False(t, gjson.Get(written, "previous_response_id").Exists())
	require.Len(t, gjson.Get(written, "input").Array(), 3)
	require.Equal(t, "assistant", gjson.Get(written, "input.1.role").String())
	metrics := svc.SnapshotOpenAIWSRetryMetrics()
	require.Equal(t, int64(1), metrics.IngressCrossConnDetectedTotal)
	require.Equal(t, int64(1), metrics.IngressCrossConnRebuildTotal)
	require.Equal(t, int64(0), metrics.IngressCrossConnBlockedTotal)
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_RejectsMessageIDAsPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
	}

	account := &Account{
		ID:          119,
		Name:        "openai-ingress-prev-validation",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"msg_123456"}`))
	cancelWrite()
	require.NoError(t, err)

	select {
	case serverErr := <-serverErrCh:
		require.Error(t, serverErr)
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
		require.Contains(t, closeErr.Reason(), "previous_response_id must be a response.id")
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
}

type openAIWSQueueDialer struct {
	mu        sync.Mutex
	conns     []openAIWSClientConn
	dialCount int
	dials     []openAIWSDialSnapshot
}

func newOpenAIWSIngressRecoveryTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	return cfg
}

func newOpenAIWSIngressRecoveryTestAccount(accountID int64) *Account {
	return &Account{
		ID:          accountID,
		Name:        "openai-ingress-recovery-boundary",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra:       map[string]any{"responses_websockets_v2_enabled": true},
	}
}

func runOpenAIWSIngressRecoveryTest(
	t *testing.T,
	svc *OpenAIGatewayService,
	account *Account,
	payload []byte,
	readEvent bool,
) ([]byte, error) {
	t.Helper()
	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req
		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancelRead()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}
		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, payload))
	cancelWrite()

	var event []byte
	if readEvent {
		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		msgType, message, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		event = message
	}
	select {
	case serverErr := <-serverErrCh:
		return event, serverErr
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 恢复边界测试结束超时")
		return nil, nil
	}
}

type openAIWSDialSnapshot struct {
	wsURL    string
	headers  http.Header
	proxyURL string
}

func (d *openAIWSQueueDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
) (openAIWSClientConn, int, http.Header, error) {
	_ = ctx
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dialCount++
	d.dials = append(d.dials, openAIWSDialSnapshot{
		wsURL:    wsURL,
		headers:  headers.Clone(),
		proxyURL: proxyURL,
	})
	if len(d.conns) == 0 {
		return nil, 503, nil, errors.New("no test conn")
	}
	conn := d.conns[0]
	if len(d.conns) > 1 {
		d.conns = d.conns[1:]
	}
	return conn, 0, nil, nil
}

func (d *openAIWSQueueDialer) DialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dialCount
}

func (d *openAIWSQueueDialer) DialSnapshots() []openAIWSDialSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	snapshots := make([]openAIWSDialSnapshot, 0, len(d.dials))
	for _, dial := range d.dials {
		snapshots = append(snapshots, openAIWSDialSnapshot{
			wsURL:    dial.wsURL,
			headers:  dial.headers.Clone(),
			proxyURL: dial.proxyURL,
		})
	}
	return snapshots
}

type openAIWSPreflightFailConn struct {
	mu         sync.Mutex
	events     [][]byte
	pingFails  bool
	writeCount int
	pingCount  int
	rawWrites  [][]byte
}

func (c *openAIWSPreflightFailConn) WriteJSON(_ context.Context, value any) error {
	c.mu.Lock()
	if payload, ok := value.(json.RawMessage); ok {
		c.rawWrites = append(c.rawWrites, append([]byte(nil), payload...))
	}
	c.writeCount++
	c.mu.Unlock()
	return nil
}

func (c *openAIWSPreflightFailConn) ReadMessage(context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) == 0 {
		return nil, io.EOF
	}
	event := c.events[0]
	c.events = c.events[1:]
	if len(c.events) == 0 {
		c.pingFails = true
	}
	return event, nil
}

func (c *openAIWSPreflightFailConn) Ping(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pingCount++
	if c.pingFails {
		return errors.New("preflight ping failed")
	}
	return nil
}

func (c *openAIWSPreflightFailConn) Close() error {
	return nil
}

func (c *openAIWSPreflightFailConn) WriteCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeCount
}

func (c *openAIWSPreflightFailConn) PingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pingCount
}

func (c *openAIWSPreflightFailConn) RawWrite(index int) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if index < 0 || index >= len(c.rawWrites) {
		return nil
	}
	return append([]byte(nil), c.rawWrites[index]...)
}

type openAIWSWriteFailAfterFirstTurnConn struct {
	mu          sync.Mutex
	events      [][]byte
	failOnWrite bool
	rawWrites   [][]byte
}

func (c *openAIWSWriteFailAfterFirstTurnConn) WriteJSON(_ context.Context, value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if payload, ok := value.(json.RawMessage); ok {
		c.rawWrites = append(c.rawWrites, append([]byte(nil), payload...))
	}
	if c.failOnWrite {
		return errors.New("write failed on stale conn")
	}
	return nil
}

func (c *openAIWSWriteFailAfterFirstTurnConn) ReadMessage(context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) == 0 {
		return nil, io.EOF
	}
	event := c.events[0]
	c.events = c.events[1:]
	if len(c.events) == 0 {
		c.failOnWrite = true
	}
	return event, nil
}

func (c *openAIWSWriteFailAfterFirstTurnConn) Ping(context.Context) error {
	return nil
}

func (c *openAIWSWriteFailAfterFirstTurnConn) Close() error {
	return nil
}

func (c *openAIWSWriteFailAfterFirstTurnConn) RawWrite(index int) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if index < 0 || index >= len(c.rawWrites) {
		return nil
	}
	return append([]byte(nil), c.rawWrites[index]...)
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ClientDisconnectStillDrainsUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	// 多个上游事件：前几个为非 terminal 事件，最后一个为 terminal。
	// 第一个事件延迟 250ms 让客户端 RST 有时间传播，使 writeClientMessage 可靠失败。
	captureConn := &openAIWSCaptureConn{
		readDelays: []time.Duration{250 * time.Millisecond, 0, 0},
		events: [][]byte{
			[]byte(`{"type":"response.created","response":{"id":"resp_ingress_disconnect","model":"gpt-5.1"}}`),
			[]byte(`{"type":"response.output_item.added","response":{"id":"resp_ingress_disconnect"}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_ingress_disconnect","model":"gpt-5.1","usage":{"input_tokens":2,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          115,
		Name:        "openai-ingress-client-disconnect",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
			"model_mapping": map[string]any{
				"custom-original-model": "gpt-5.1",
			},
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	resultCh := make(chan *OpenAIForwardResult, 1)
	hooks := &OpenAIWSIngressHooks{
		AfterTurn: func(_ int, result *OpenAIForwardResult, turnErr error) {
			if turnErr == nil && result != nil {
				resultCh <- result
			}
		},
	}
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"custom-original-model","stream":false,"service_tier":"flex"}`))
	cancelWrite()
	require.NoError(t, err)
	// 立即关闭客户端，模拟客户端在 relay 期间断连。
	require.NoError(t, clientConn.CloseNow(), "模拟 ingress 客户端提前断连")

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr, "客户端断连后应继续 drain 上游直到 terminal 或正常结束")
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	select {
	case result := <-resultCh:
		require.Equal(t, "resp_ingress_disconnect", result.RequestID)
		require.Equal(t, 2, result.Usage.InputTokens)
		require.Equal(t, 1, result.Usage.OutputTokens)
		require.NotNil(t, result.ServiceTier)
		require.Equal(t, "flex", *result.ServiceTier)
	case <-time.After(2 * time.Second):
		t.Fatal("未收到断连后的 turn 结果回调")
	}
}
