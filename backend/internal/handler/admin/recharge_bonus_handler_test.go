//go:build unit

package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

type rechargeBonusAPIResponse struct {
	Code int
	Data json.RawMessage
}

func newRechargeBonusHandlerTestServer(t *testing.T) (*gin.Engine, *dbent.Client) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:recharge_bonus_handler_%d?mode=memory&cache=shared&_fk=1", time.Now().UnixNano()))
	require.NoError(t, err)
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	bonusService := service.NewRechargeBonusService(client, nil, nil, nil)
	paymentService := service.NewPaymentService(client, nil, nil, nil, nil, nil, nil, nil, nil)
	paymentService.SetRechargeBonusService(bonusService)
	handler := NewPaymentHandler(paymentService, nil)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/admin/payment/recharge-bonus-campaigns", handler.ListRechargeBonusCampaigns)
	router.POST("/api/v1/admin/payment/recharge-bonus-campaigns", handler.CreateRechargeBonusCampaign)
	router.PUT("/api/v1/admin/payment/recharge-bonus-campaigns/:id", handler.UpdateRechargeBonusCampaign)
	router.DELETE("/api/v1/admin/payment/recharge-bonus-campaigns/:id", handler.DeleteRechargeBonusCampaign)
	return router, client
}

func performRechargeBonusRequest(t *testing.T, router http.Handler, method, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		require.NoError(t, json.NewEncoder(&body).Encode(payload))
	}
	request := httptest.NewRequest(method, path, &body)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestRechargeBonusCampaignHandlers_CRUDAndUTCContract(t *testing.T) {
	router, client := newRechargeBonusHandlerTestServer(t)
	startAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	input := service.RechargeBonusCampaignInput{
		Name:               "跨时区充值活动",
		Description:        "第一行\n第二行",
		StartAt:            startAt,
		EndAt:              startAt.Add(48 * time.Hour),
		ParticipationLimit: 2,
		Tiers: []service.RechargeBonusTier{
			{MinAmount: 10, MaxAmount: 100, MinRate: 5, MaxRate: 10},
		},
	}

	createdResponse := performRechargeBonusRequest(t, router, http.MethodPost, "/api/v1/admin/payment/recharge-bonus-campaigns", input)
	require.Equal(t, http.StatusCreated, createdResponse.Code)
	require.Contains(t, createdResponse.Body.String(), startAt.Format(time.RFC3339))
	var createdEnvelope rechargeBonusAPIResponse
	require.NoError(t, json.Unmarshal(createdResponse.Body.Bytes(), &createdEnvelope))
	var created service.RechargeBonusCampaign
	require.NoError(t, json.Unmarshal(createdEnvelope.Data, &created))
	require.Equal(t, input.Name, created.Name)
	require.Equal(t, input.Description, created.Description)

	stored, err := client.RechargeBonusCampaign.Get(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, time.UTC, stored.StartAt.Location())
	require.True(t, stored.StartAt.Equal(startAt))

	listResponse := performRechargeBonusRequest(t, router, http.MethodGet, "/api/v1/admin/payment/recharge-bonus-campaigns", nil)
	require.Equal(t, http.StatusOK, listResponse.Code)
	var listEnvelope rechargeBonusAPIResponse
	require.NoError(t, json.Unmarshal(listResponse.Body.Bytes(), &listEnvelope))
	var listed []service.RechargeBonusCampaign
	require.NoError(t, json.Unmarshal(listEnvelope.Data, &listed))
	require.Equal(t, input.Description, listed[0].Description)

	input.Description = "已更新"
	updatePath := fmt.Sprintf("/api/v1/admin/payment/recharge-bonus-campaigns/%d", created.ID)
	updateResponse := performRechargeBonusRequest(t, router, http.MethodPut, updatePath, input)
	require.Equal(t, http.StatusOK, updateResponse.Code)
	require.Contains(t, updateResponse.Body.String(), "已更新")

	deleteResponse := performRechargeBonusRequest(t, router, http.MethodDelete, updatePath, nil)
	require.Equal(t, http.StatusOK, deleteResponse.Code)
	_, err = client.RechargeBonusCampaign.Get(context.Background(), created.ID)
	require.True(t, dbent.IsNotFound(err))
}

func TestRechargeBonusCampaignHandlers_RejectInvalidDescription(t *testing.T) {
	router, _ := newRechargeBonusHandlerTestServer(t)
	startAt := time.Now().UTC().Add(24 * time.Hour)
	input := service.RechargeBonusCampaignInput{
		Name:        "活动",
		Description: string(bytes.Repeat([]byte("a"), 1001)),
		StartAt:     startAt,
		EndAt:       startAt.Add(time.Hour),
		Tiers: []service.RechargeBonusTier{
			{MinAmount: 0, MaxAmount: 100, MinRate: 5, MaxRate: 5},
		},
	}

	response := performRechargeBonusRequest(t, router, http.MethodPost, "/api/v1/admin/payment/recharge-bonus-campaigns", input)
	require.Equal(t, http.StatusBadRequest, response.Code)
}
