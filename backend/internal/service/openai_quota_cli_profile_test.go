package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type recordingQuotaHTTPUpstream struct {
	request *http.Request
}

func (s *recordingQuotaHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	s.request = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}, nil
}

func (s *recordingQuotaHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, accountID, accountConcurrency)
}

func TestBuildCodexCommonHeadersMatchesCLI0149BackendClient(t *testing.T) {
	headers := buildCodexCommonHeaders("token", "workspace", true)
	require.Equal(t, "Bearer token", headers["authorization"])
	require.Equal(t, "workspace", headers["chatgpt-account-id"])
	require.Equal(t, codexCLI0149WindowsUserAgent, headers["user-agent"])
	require.Equal(t, "true", headers["x-openai-fedramp"])
	for _, forbidden := range []string{"originator", "openai-beta", "oai-language", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest", "priority"} {
		require.NotContains(t, headers, forbidden)
	}
}

func TestOpenAIQuotaUpstreamClientUsesOpenAITransportProfile(t *testing.T) {
	upstream := &recordingQuotaHTTPUpstream{}
	client := &openAIQuotaUpstreamClient{upstream: upstream, accountID: 11, accountConcurrency: 2}
	headers := buildCodexCommonHeaders("token", "workspace", false)

	response, err := client.Do(context.Background(), http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", headers, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NotNil(t, upstream.request)
	require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(upstream.request.Context()))
	require.True(t, HTTPUpstreamRedirectsDisabled(upstream.request.Context()))
	require.Equal(t, codexCLI0149WindowsUserAgent, upstream.request.Header.Get("User-Agent"))
	require.Empty(t, upstream.request.Header.Get("Originator"))
}

func TestNewOpenAIQuotaRequestClientUsesCredentialAccountPool(t *testing.T) {
	parentID := int64(10)
	parent := &Account{ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 3}
	shadow := &Account{ID: 20, ParentAccountID: &parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}
	repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{parent.ID: parent, shadow.ID: shadow}}
	svc := NewOpenAIQuotaService(repo, nil, nil, nil)
	svc.SetHTTPUpstream(&recordingQuotaHTTPUpstream{})

	client, err := svc.newOpenAIQuotaRequestClient(context.Background(), "", shadow.ID)
	require.NoError(t, err)
	upstreamClient, ok := client.(*openAIQuotaUpstreamClient)
	require.True(t, ok)
	require.Equal(t, parent.ID, upstreamClient.accountID)
	require.Equal(t, parent.Concurrency, upstreamClient.accountConcurrency)
}
