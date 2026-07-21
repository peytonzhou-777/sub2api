//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubLimitedCreditRepository struct {
	service.LimitedCreditRepository
	available float64
	err       error
	calls     int
}

func (s *stubLimitedCreditRepository) GetAvailableAmount(_ context.Context, _ int64) (float64, error) {
	s.calls++
	return s.available, s.err
}

func TestAPIKeyAuthAllowsLimitedCreditWithoutOrdinaryBalance(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{ID: 301, Role: service.RoleUser, Status: service.StatusActive, Balance: 0, Concurrency: 1}
	apiKey := &service.APIKey{ID: 401, UserID: user.ID, Key: "limited-credit-main", Status: service.StatusActive, User: user}
	apiKeyService := service.NewAPIKeyService(&stubApiKeyRepo{
		getByKey: func(_ context.Context, key string) (*service.APIKey, error) {
			require.Equal(t, apiKey.Key, key)
			clone := *apiKey
			userClone := *user
			clone.User = &userClone
			return &clone, nil
		},
	}, nil, nil, nil, nil, nil, &config.Config{})

	limitedRepo := &stubLimitedCreditRepository{available: 2.5}
	billingCacheService := service.NewBillingCacheService(
		nil,
		&stubUserRepo{getByID: func(_ context.Context, id int64) (*service.User, error) {
			require.Equal(t, user.ID, id)
			clone := *user
			return &clone, nil
		}},
		nil,
		nil,
		nil,
		nil,
		&config.Config{},
		nil,
		limitedRepo,
	)
	t.Cleanup(billingCacheService.Stop)

	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddlewareWithBillingCache(apiKeyService, nil, billingCacheService, &config.Config{})))
	router.GET("/t", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/t", nil)
	request.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, limitedRepo.calls)
}

func TestGoogleAPIKeyAuthAllowsLimitedCreditWithoutOrdinaryBalance(t *testing.T) {
	gin.SetMode(gin.TestMode)

	user := &service.User{ID: 302, Role: service.RoleUser, Status: service.StatusActive, Balance: 0, Concurrency: 1}
	apiKey := &service.APIKey{ID: 402, UserID: user.ID, Key: "limited-credit-google", Status: service.StatusActive, User: user}
	apiKeyService := service.NewAPIKeyService(&stubApiKeyRepo{
		getByKey: func(_ context.Context, key string) (*service.APIKey, error) {
			require.Equal(t, apiKey.Key, key)
			clone := *apiKey
			userClone := *user
			clone.User = &userClone
			return &clone, nil
		},
	}, nil, nil, nil, nil, nil, &config.Config{})

	limitedRepo := &stubLimitedCreditRepository{available: 3.5}
	billingCacheService := service.NewBillingCacheService(
		nil,
		&stubUserRepo{getByID: func(_ context.Context, id int64) (*service.User, error) {
			require.Equal(t, user.ID, id)
			clone := *user
			return &clone, nil
		}},
		nil,
		nil,
		nil,
		nil,
		&config.Config{},
		nil,
		limitedRepo,
	)
	t.Cleanup(billingCacheService.Stop)

	router := gin.New()
	router.Use(APIKeyAuthWithSubscriptionGoogleAndBillingCache(apiKeyService, nil, billingCacheService, &config.Config{}))
	router.GET("/v1beta/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1beta/test", nil)
	request.Header.Set("x-goog-api-key", apiKey.Key)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 1, limitedRepo.calls)
}
