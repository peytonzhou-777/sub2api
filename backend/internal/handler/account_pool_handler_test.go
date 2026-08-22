package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func TestParseAccountPoolQueryDefaultsToNewestAccountFirst(t *testing.T) {
	c := newAccountPoolQueryContext(t, "/api/v1/account-pool")
	page, pageSize, query, ok := parseAccountPoolQuery(c)
	if !ok || page != 1 || pageSize != 20 || query.SortBy != service.AccountPoolSortByID || query.SortOrder != service.AccountPoolSortDesc {
		t.Fatalf("默认查询参数不正确: page=%d pageSize=%d query=%+v ok=%v", page, pageSize, query, ok)
	}
}

func TestParseAccountPoolQueryAcceptsStatusSortAndFilter(t *testing.T) {
	c := newAccountPoolQueryContext(t, "/api/v1/account-pool?page=2&page_size=50&status=rate_limited&sort_by=status&sort_order=asc")
	page, pageSize, query, ok := parseAccountPoolQuery(c)
	if !ok || page != 2 || pageSize != 50 || query.Status != "rate_limited" ||
		query.SortBy != service.AccountPoolSortByStatus || query.SortOrder != service.AccountPoolSortAsc {
		t.Fatalf("状态查询参数不正确: page=%d pageSize=%d query=%+v ok=%v", page, pageSize, query, ok)
	}
}

func TestParseAccountPoolQueryRejectsUnknownStatus(t *testing.T) {
	c := newAccountPoolQueryContext(t, "/api/v1/account-pool?status=unknown")
	if _, _, _, ok := parseAccountPoolQuery(c); ok {
		t.Fatal("未知公开状态不应通过查询参数校验")
	}
}

func TestParseAccountPoolQueryAcceptsUserRelationFilter(t *testing.T) {
	c := newAccountPoolQueryContext(t, "/api/v1/account-pool?relation=seven_day_contact")
	_, _, query, ok := parseAccountPoolQuery(c)
	if !ok || query.Relation != service.AccountPoolRelationSevenDayContact {
		t.Fatalf("用户关系筛选参数不正确: query=%+v ok=%v", query, ok)
	}
}

func TestParseAccountPoolQueryAcceptsPrimaryResidenceFilter(t *testing.T) {
	c := newAccountPoolQueryContext(t, "/api/v1/account-pool?relation=primary_residence")
	_, _, query, ok := parseAccountPoolQuery(c)
	if !ok || query.Relation != service.AccountPoolRelationPrimaryResidence {
		t.Fatalf("首选居住账号筛选参数不正确: query=%+v ok=%v", query, ok)
	}
}

func TestParseAccountPoolQueryRejectsUnknownUserRelation(t *testing.T) {
	c := newAccountPoolQueryContext(t, "/api/v1/account-pool?relation=reserved_only")
	if _, _, _, ok := parseAccountPoolQuery(c); ok {
		t.Fatal("未知用户关系不应通过查询参数校验")
	}
}

func newAccountPoolQueryContext(t *testing.T, target string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", target, nil)
	return c
}
