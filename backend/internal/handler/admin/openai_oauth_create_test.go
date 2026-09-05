//go:build unit

package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type oauthCreateClient struct {
	service.OpenAIOAuthClient
	response *openai.TokenResponse
	err      error
	clientID string
}

func (s *oauthCreateClient) ExchangeCode(_ context.Context, _, _, _, _, clientID string) (*openai.TokenResponse, error) {
	s.clientID = clientID
	return s.response, s.err
}

func (s *oauthCreateClient) RefreshTokenWithClientID(_ context.Context, _, _, clientID string) (*openai.TokenResponse, error) {
	s.clientID = clientID
	return s.response, s.err
}

// 捕获加密输入以验证 Token 只进入 Persona，而不出现在账号 JSON。
type oauthCreateEncryptor struct{ plaintext string }

func (s *oauthCreateEncryptor) Encrypt(plaintext string) (string, error) {
	s.plaintext = plaintext
	return "encrypted-persona", nil
}
func (*oauthCreateEncryptor) Decrypt(string) (string, error) { return "", nil }

type oauthCreateAdmin struct{ stubAdminService }

func (s *oauthCreateAdmin) CreateAccount(ctx context.Context, input *service.CreateAccountInput) (*service.Account, error) {
	account, err := s.stubAdminService.CreateAccount(ctx, input)
	if account != nil {
		account.Platform, account.Type = input.Platform, input.Type
		account.Credentials, account.Extra = input.Credentials, input.Extra
	}
	return account, err
}

func postOAuthCreate(t *testing.T, h *OpenAIOAuthHandler, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	router := gin.New()
	router.POST("/create-from-oauth", h.CreateAccountFromOAuth)
	request := httptest.NewRequest(http.MethodPost, "/create-from-oauth", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestOpenAIOAuthCreate_PrimaryPersonaAndSettings(t *testing.T) {
	for _, method := range []string{"code", "rt", "mobile-rt", "rt-without-rotation"} {
		t.Run(method, func(t *testing.T) {
			claims := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"owner@example.com","https://api.openai.com/auth":{"chatgpt_account_id":"upstream-account","chatgpt_plan_type":"pro"}}`))
			token := &openai.TokenResponse{AccessToken: "runtime-access", RefreshToken: "runtime-refresh", IDToken: "e30." + claims + ".signature", ExpiresIn: 3600}
			client := &oauthCreateClient{response: token}
			encryptor := &oauthCreateEncryptor{}
			svc := service.ProvideOpenAIOAuthService(nil, client, nil, nil, nil, encryptor, nil)
			t.Cleanup(svc.Stop)
			admin := &oauthCreateAdmin{}
			h := NewOpenAIOAuthHandler(svc, admin, nil, nil)
			payload := map[string]any{
				"name": "new account", "notes": "note", "concurrency": 7, "priority": 12,
				"load_factor": 4, "rate_multiplier": 0.5, "group_ids": []int64{2, 3},
				"expires_at": int64(1900000000), "auto_pause_on_expired": false,
				"extra": map[string]any{"codex_fingerprint_mode": "session", "openai_long_context_billing_enabled": true},
				"credential_extras": map[string]any{
					"model_mapping": map[string]any{"public": "upstream"}, "compact_model_mapping": map[string]any{"compact": "upstream"},
					"temp_unschedulable_enabled": true, "access_token": "injected-access", "refresh_token": "injected-refresh",
					"id_token": "injected-id", "client_id": "injected-client", "chatgpt_account_id": "injected-account", "auth_mode": "api_key",
				},
			}
			expectedClient := openai.ClientID
			if method == "code" {
				auth, err := svc.GenerateAuthURL(context.Background(), nil, "", service.PlatformOpenAI)
				require.NoError(t, err)
				parsed, err := url.Parse(auth.AuthURL)
				require.NoError(t, err)
				payload["session_id"], payload["code"], payload["state"] = auth.SessionID, "code", parsed.Query().Get("state")
			} else {
				payload["refresh_token"] = "input-refresh"
				if method == "mobile-rt" {
					expectedClient = "app_LlGpXReQgckcGGUo2JrYvtJK"
					payload["client_id"] = expectedClient
				}
				if method == "rt-without-rotation" {
					token.RefreshToken = ""
				}
			}
			recorder := postOAuthCreate(t, h, payload)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			require.Len(t, admin.createdAccounts, 1)
			input := admin.createdAccounts[0]
			require.NotNil(t, input.PrimaryOpenAIPersona)
			require.Equal(t, "upstream-account", input.PrimaryOpenAIPersona.ChatGPTAccountID)
			require.Equal(t, expectedClient, input.PrimaryOpenAIPersona.OAuthClientID)
			require.Equal(t, expectedClient, client.clientID)
			require.Equal(t, "note", *input.Notes)
			require.Equal(t, 7, input.Concurrency)
			require.Equal(t, 12, input.Priority)
			require.Equal(t, 4, *input.LoadFactor)
			require.Equal(t, 0.5, *input.RateMultiplier)
			require.Equal(t, []int64{2, 3}, input.GroupIDs)
			require.Equal(t, int64(1900000000), *input.ExpiresAt)
			require.False(t, *input.AutoPauseOnExpired)
			require.Equal(t, "session", input.Extra["codex_fingerprint_mode"])
			require.Equal(t, true, input.Extra["openai_long_context_billing_enabled"])
			require.Equal(t, "owner@example.com", input.Extra["email"])
			require.Equal(t, map[string]any{"public": "upstream"}, input.Credentials["model_mapping"])
			require.Equal(t, map[string]any{"compact": "upstream"}, input.Credentials["compact_model_mapping"])
			require.Equal(t, true, input.Credentials["temp_unschedulable_enabled"])
			require.Contains(t, encryptor.plaintext, "runtime-access")
			if method == "rt-without-rotation" {
				require.Contains(t, encryptor.plaintext, "input-refresh")
			} else {
				require.Contains(t, encryptor.plaintext, "runtime-refresh")
			}
			for _, key := range []string{"access_token", "refresh_token", "id_token", "client_id", "expires_at", "auth_mode"} {
				require.NotContains(t, input.Credentials, key)
			}
			require.NotContains(t, recorder.Body.String(), "runtime-access")
			require.NotContains(t, recorder.Body.String(), "runtime-refresh")
			require.NotContains(t, recorder.Body.String(), "injected-")
		})
	}
}

func TestOpenAIOAuthCreate_RejectsInvalidInputBeforeExchange(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"session_id":"s","code":"c"}`,
		`{"refresh_token":"rt","code":"c"}`,
		`{"refresh_token":"rt","concurrency":-1}`,
		`{"refresh_token":"rt","priority":-1}`,
		`{"refresh_token":"rt","load_factor":10001}`,
		`{"refresh_token":"rt","rate_multiplier":-1}`,
		`{"refresh_token":"rt","extra":{"openai_long_context_billing_enabled":"true"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(body), &payload))
			admin := &oauthCreateAdmin{}
			recorder := postOAuthCreate(t, NewOpenAIOAuthHandler(nil, admin, nil, nil), payload)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
			require.Empty(t, admin.createdAccounts)
		})
	}
}

func TestOpenAIOAuthCreate_ExchangeFailureDoesNotCreateAccount(t *testing.T) {
	client := &oauthCreateClient{err: errors.New("upstream unavailable")}
	svc := service.NewOpenAIOAuthService(nil, client)
	t.Cleanup(svc.Stop)
	admin := &oauthCreateAdmin{}
	recorder := postOAuthCreate(t, NewOpenAIOAuthHandler(svc, admin, nil, nil), map[string]any{"refresh_token": "rt"})
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Empty(t, admin.createdAccounts)
}

func TestOpenAIOAuthCreate_PersonaAndPersistenceFailures(t *testing.T) {
	for _, failure := range []string{"incomplete-token", "encryptor-unavailable", "create-failed", "invalid-state"} {
		t.Run(failure, func(t *testing.T) {
			claims := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"owner@example.com","https://api.openai.com/auth":{"chatgpt_account_id":"upstream-account"}}`))
			client := &oauthCreateClient{response: &openai.TokenResponse{AccessToken: "runtime-access", RefreshToken: "runtime-refresh", IDToken: "e30." + claims + ".signature"}}
			var encryptor service.SecretEncryptor = &oauthCreateEncryptor{}
			if failure == "incomplete-token" {
				client.response.IDToken = ""
			}
			if failure == "encryptor-unavailable" {
				encryptor = nil
			}
			svc := service.ProvideOpenAIOAuthService(nil, client, nil, nil, nil, encryptor, nil)
			t.Cleanup(svc.Stop)
			admin := &oauthCreateAdmin{}
			if failure == "create-failed" {
				admin.createAccountErr = errors.New("transaction failed")
			}
			payload := map[string]any{"refresh_token": "input-refresh"}
			if failure == "invalid-state" {
				auth, err := svc.GenerateAuthURL(context.Background(), nil, "", service.PlatformOpenAI)
				require.NoError(t, err)
				payload = map[string]any{"session_id": auth.SessionID, "code": "code", "state": "wrong-state"}
			}
			recorder := postOAuthCreate(t, NewOpenAIOAuthHandler(svc, admin, nil, nil), payload)
			require.GreaterOrEqual(t, recorder.Code, http.StatusBadRequest)
			if failure == "create-failed" {
				require.Len(t, admin.createdAccounts, 1)
				require.NotNil(t, admin.createdAccounts[0].PrimaryOpenAIPersona)
			} else {
				require.Empty(t, admin.createdAccounts)
			}
			if failure == "invalid-state" {
				require.Empty(t, client.clientID, "invalid state must not consume the code")
			}
			require.NotContains(t, recorder.Body.String(), "runtime-access")
			require.NotContains(t, recorder.Body.String(), "runtime-refresh")
		})
	}
}
